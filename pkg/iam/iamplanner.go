package iam

import (
	"sync"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMBinding represents a single, planned IAM permission, linking a principal (who)
// to a role (what) on a specific resource (where).
type IAMBinding struct {
	// ResourceType is a key identifying the type of cloud resource (e.g., "pubsub_topic", "gcs_bucket").
	ResourceType string
	// ResourceID is the unique name of the resource (e.g., "my-data-topic").
	ResourceID string
	// Role is the full name of the IAM role to be granted (e.g., "roles/pubsub.publisher").
	Role string
	// ResourceLocation is the geographic location/region of the resource, which is required
	// for applying IAM policies to regional services like Cloud Run.
	ResourceLocation string
}

// RolePlanner is responsible for analyzing a microservice architecture and determining
// the complete set of IAM bindings required for all services to function correctly.
type RolePlanner struct {
	logger zerolog.Logger
}

// NewRolePlanner creates a new planner.
func NewRolePlanner(logger zerolog.Logger) *RolePlanner {
	return &RolePlanner{
		logger: logger.With().Str("component", "RolePlanner").Logger(),
	}
}

// PlanRolesForServiceDirector inspects the architecture and synthesizes the high-level
// administrative roles needed by the main ServiceDirector service account to manage all resources.
func (p *RolePlanner) PlanRolesForServiceDirector(arch *servicemanager.MicroserviceArchitecture) ([]string, error) {
	p.logger.Info().Str("architecture", arch.Environment.Name).Msg("Planning required IAM roles for ServiceDirector...")

	// Use a map to automatically handle duplicate roles.
	requiredRoles := make(map[string]struct{})
	for _, dataflow := range arch.Dataflows {
		resources := dataflow.Resources
		if len(resources.Topics) > 0 || len(resources.Subscriptions) > 0 {
			requiredRoles["roles/pubsub.admin"] = struct{}{}
		}
		if len(resources.GCSBuckets) > 0 {
			requiredRoles["roles/storage.admin"] = struct{}{}
		}
		if len(resources.BigQueryDatasets) > 0 || len(resources.BigQueryTables) > 0 {
			requiredRoles["roles/bigquery.admin"] = struct{}{}
		}
		// If any service in the dataflow uses secrets, the ServiceDirector needs
		// permission to manage IAM policies on those secrets.
		for _, service := range dataflow.Services {
			if service.Deployment != nil && len(service.Deployment.SecretEnvironmentVars) > 0 {
				requiredRoles["roles/secretmanager.admin"] = struct{}{}
				break // Only need to add the role once.
			}
		}
	}

	rolesSlice := make([]string, 0, len(requiredRoles))
	for role := range requiredRoles {
		rolesSlice = append(rolesSlice, role)
	}

	p.logger.Info().Strs("roles", rolesSlice).Msg("ServiceDirector IAM role plan complete.")
	return rolesSlice, nil
}

// PlanRolesForApplicationServices generates a detailed IAM plan for all application services.
// It maps each service account to a list of specific IAM bindings it requires.
func (p *RolePlanner) PlanRolesForApplicationServices(arch *servicemanager.MicroserviceArchitecture) (map[string][]IAMBinding, error) {
	p.logger.Info().Str("architecture", arch.Environment.Name).Msg("Planning required IAM roles for all application services...")

	finalPlan := make(map[string][]IAMBinding)
	var mu sync.Mutex // Mutex to protect concurrent writes to the finalPlan map.

	for _, dataflow := range arch.Dataflows {
		// First, plan roles from the explicit Pub/Sub producer/consumer links.
		p.planPubSubLinkRoles(dataflow, finalPlan, &mu)

		// Next, iterate through each service to plan for its other dependencies and resource usage.
		for serviceName, serviceSpec := range dataflow.Services {
			p.planDependencyRoles(arch, serviceName, serviceSpec, finalPlan, &mu)
			p.planResourceUsageRoles(serviceName, serviceSpec, dataflow.Resources, finalPlan, &mu)
		}
	}

	p.logger.Info().Int("services_planned", len(finalPlan)).Msg("Application service IAM role plan complete.")
	return finalPlan, nil
}

// planPubSubLinkRoles plans IAM roles based on the `ProducerService` and `ConsumerService`
// fields in the Pub/Sub resource definitions.
func (p *RolePlanner) planPubSubLinkRoles(dataflow servicemanager.ResourceGroup, plan map[string][]IAMBinding, mu *sync.Mutex) {
	// Plan Publisher roles from Topics based on `ProducerService`
	for _, topic := range dataflow.Resources.Topics {
		if topic.ProducerService != "" {
			if service, ok := dataflow.Services[topic.ProducerService]; ok {
				p.logger.Info().Str("service", service.Name).Str("topic", topic.Name).Msg("Planning explicit publisher and viewer roles from ProducerService link")
				// A publisher needs both publisher and viewer roles on the topic.
				pubBinding := IAMBinding{ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: "roles/pubsub.publisher"}
				viewBinding := IAMBinding{ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: "roles/pubsub.viewer"}
				addBindingToPlan(service.ServiceAccount, pubBinding, plan, mu)
				addBindingToPlan(service.ServiceAccount, viewBinding, plan, mu)
			}
		}
	}

	// Plan Subscriber roles from Subscriptions based on `ConsumerService`
	for _, sub := range dataflow.Resources.Subscriptions {
		if sub.ConsumerService != "" {
			if service, ok := dataflow.Services[sub.ConsumerService]; ok {
				p.logger.Info().Str("service", service.Name).Str("subscription", sub.Name).Msg("Planning explicit subscriber and topic viewer roles from ConsumerService link")
				// A subscriber needs the subscriber role on the subscription itself.
				subBinding := IAMBinding{ResourceType: "pubsub_subscription", ResourceID: sub.Name, Role: "roles/pubsub.subscriber"}
				addBindingToPlan(service.ServiceAccount, subBinding, plan, mu)
				// A subscriber also typically needs to view the topic to which it is subscribed.
				viewTopicBinding := IAMBinding{ResourceType: "pubsub_topic", ResourceID: sub.Topic, Role: "roles/pubsub.viewer"}
				addBindingToPlan(service.ServiceAccount, viewTopicBinding, plan, mu)
			}
		}
	}
}

// planDependencyRoles infers roles needed for services to communicate with each other.
func (p *RolePlanner) planDependencyRoles(arch *servicemanager.MicroserviceArchitecture, serviceName string, serviceSpec servicemanager.ServiceSpec, plan map[string][]IAMBinding, mu *sync.Mutex) {
	// Infer Cloud Run Invoker role from the `dependencies` list.
	if len(serviceSpec.Dependencies) > 0 {
		for _, dependencyName := range serviceSpec.Dependencies {
			p.logger.Info().Str("service", serviceName).Str("dependency", dependencyName).Msg("Planning Cloud Run invoker role")
			dependencyRegion := findServiceRegion(arch, dependencyName)
			binding := IAMBinding{
				ResourceType:     "cloudrun_service",
				ResourceID:       dependencyName,
				Role:             "roles/run.invoker",
				ResourceLocation: dependencyRegion,
			}
			addBindingToPlan(serviceSpec.ServiceAccount, binding, plan, mu)
		}
	}
}

// planResourceUsageRoles infers roles from how services are configured to use resources.
func (p *RolePlanner) planResourceUsageRoles(serviceName string, serviceSpec servicemanager.ServiceSpec, resources servicemanager.CloudResourcesSpec, plan map[string][]IAMBinding, mu *sync.Mutex) {
	// Scan all resources for explicit `iam_access_policy` blocks for this service.
	scanResourcesForExplicitPolicies(serviceName, serviceSpec.ServiceAccount, resources, plan, mu)

	// Infer roles from conventions in the deployment spec.
	if serviceSpec.Deployment != nil {
		envVars := serviceSpec.Deployment.EnvironmentVars
		if datasetID, ok := envVars["BIGQUERY_DATASET"]; ok {
			p.logger.Info().Str("service", serviceName).Str("dataset", datasetID).Msg("Inferred BigQuery dataEditor role")
			binding := IAMBinding{ResourceType: "bigquery_dataset", ResourceID: datasetID, Role: "roles/bigquery.dataEditor"}
			addBindingToPlan(serviceSpec.ServiceAccount, binding, plan, mu)
		}
		if bucketName, ok := envVars["GCS_BUCKET_NAME"]; ok {
			p.logger.Info().Str("service", serviceName).Str("bucket", bucketName).Msg("Inferred Storage objectAdmin role")
			binding := IAMBinding{ResourceType: "gcs_bucket", ResourceID: bucketName, Role: "roles/storage.objectAdmin"}
			addBindingToPlan(serviceSpec.ServiceAccount, binding, plan, mu)
		}
		if len(serviceSpec.Deployment.SecretEnvironmentVars) > 0 {
			for _, secretVar := range serviceSpec.Deployment.SecretEnvironmentVars {
				p.logger.Info().Str("service", serviceName).Str("secret", secretVar.ValueFrom).Msg("Inferred Secret Manager secretAccessor role")
				binding := IAMBinding{ResourceType: "secret", ResourceID: secretVar.ValueFrom, Role: "roles/secretmanager.secretAccessor"}
				addBindingToPlan(serviceSpec.ServiceAccount, binding, plan, mu)
			}
		}
	}
}

// scanResourcesForExplicitPolicies checks all resource types for explicit IAM policies.
func scanResourcesForExplicitPolicies(serviceName, serviceAccount string, resources servicemanager.CloudResourcesSpec, plan map[string][]IAMBinding, mu *sync.Mutex) {
	for _, topic := range resources.Topics {
		for _, policy := range topic.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: policy.Role}
				addBindingToPlan(serviceAccount, binding, plan, mu)
			}
		}
	}
	for _, sub := range resources.Subscriptions {
		for _, policy := range sub.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{ResourceType: "pubsub_subscription", ResourceID: sub.Name, Role: policy.Role}
				addBindingToPlan(serviceAccount, binding, plan, mu)
			}
		}
	}
	for _, bucket := range resources.GCSBuckets {
		for _, policy := range bucket.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{ResourceType: "gcs_bucket", ResourceID: bucket.Name, Role: policy.Role}
				addBindingToPlan(serviceAccount, binding, plan, mu)
			}
		}
	}
	for _, dataset := range resources.BigQueryDatasets {
		for _, policy := range dataset.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{ResourceType: "bigquery_dataset", ResourceID: dataset.Name, Role: policy.Role}
				addBindingToPlan(serviceAccount, binding, plan, mu)
			}
		}
	}
}

// addBindingToPlan is a thread-safe helper to add a new binding to our final plan.
func addBindingToPlan(serviceAccount string, binding IAMBinding, plan map[string][]IAMBinding, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	plan[serviceAccount] = append(plan[serviceAccount], binding)
}

// findServiceRegion searches the architecture for a service by name and returns its region.
func findServiceRegion(arch *servicemanager.MicroserviceArchitecture, serviceName string) string {
	if arch.ServiceManagerSpec.Name == serviceName && arch.ServiceManagerSpec.Deployment != nil {
		if arch.ServiceManagerSpec.Deployment.Region != "" {
			return arch.ServiceManagerSpec.Deployment.Region
		}
	}
	for _, df := range arch.Dataflows {
		if svc, ok := df.Services[serviceName]; ok && svc.Deployment != nil {
			if svc.Deployment.Region != "" {
				return svc.Deployment.Region
			}
		}
	}
	return arch.Environment.Region
}
