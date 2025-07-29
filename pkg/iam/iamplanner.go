package iam

import (
	"sync"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMBinding represents a single "who gets what on where" permission.
type IAMBinding struct {
	ResourceType     string
	ResourceID       string
	Role             string
	ResourceLocation string // The location/region of the resource, e.g., "europe-west2"
}

// RolePlanner is responsible for analyzing a microservice architecture
// and determining the necessary IAM roles required to manage it.
type RolePlanner struct {
	logger zerolog.Logger
}

// NewRolePlanner creates a new planner.
func NewRolePlanner(logger zerolog.Logger) *RolePlanner {
	return &RolePlanner{
		logger: logger.With().Str("component", "RolePlanner").Logger(),
	}
}

// PlanRolesForServiceDirector inspects the architecture and synthesizes the roles
// needed by the main ServiceDirector service account to manage all resources.
func (p *RolePlanner) PlanRolesForServiceDirector(arch *servicemanager.MicroserviceArchitecture) ([]string, error) {
	p.logger.Info().Str("architecture", arch.Name).Msg("Planning required IAM roles for ServiceDirector...")

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
		// Add check for secrets to give the Service Director permission to manage IAM on them.
		for _, service := range dataflow.Services {
			if service.Deployment != nil && len(service.Deployment.SecretEnvironmentVars) > 0 {
				requiredRoles["roles/secretmanager.admin"] = struct{}{}
				break // Only need to add the role once per dataflow
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

// PlanRolesForApplicationServices now infers roles from a combination of explicit
// resource links (for Pub/Sub) and environment variables for other services.
func (p *RolePlanner) PlanRolesForApplicationServices(arch *servicemanager.MicroserviceArchitecture) (map[string][]IAMBinding, error) {
	p.logger.Info().Str("architecture", arch.Name).Msg("Planning required IAM roles for all application services...")

	finalPlan := make(map[string][]IAMBinding)
	var mu sync.Mutex

	for _, dataflow := range arch.Dataflows {
		// REFACTOR: Plan Pub/Sub roles based on explicit links in the resource definitions.
		// This is more reliable than inferring from environment variables.
		p.planPubSubRolesFromResources(dataflow, finalPlan, &mu)

		// Plan other roles based on service-level definitions (dependencies, other env vars).
		for serviceName, serviceSpec := range dataflow.Services {
			if serviceSpec.Deployment != nil {
				envVars := serviceSpec.Deployment.EnvironmentVars

				// Infer BigQuery Data Editor role
				if datasetID, ok := envVars["BIGQUERY_DATASET"]; ok {
					p.logger.Info().Str("service", serviceName).Str("dataset", datasetID).Msg("Inferred bigquery data editor role")
					binding := IAMBinding{ResourceType: "bigquery_dataset", ResourceID: datasetID, Role: "roles/bigquery.dataEditor"}
					addBindingToPlan(serviceSpec.ServiceAccount, binding, finalPlan, &mu)
				}
				// Infer GCS Object Admin role
				if bucketName, ok := envVars["GCS_BUCKET_NAME"]; ok {
					p.logger.Info().Str("service", serviceName).Str("bucket", bucketName).Msg("Inferred storage object admin role")
					binding := IAMBinding{ResourceType: "gcs_bucket", ResourceID: bucketName, Role: "roles/storage.objectAdmin"}
					addBindingToPlan(serviceSpec.ServiceAccount, binding, finalPlan, &mu)
				}
				// Infer Secret Accessor role
				if len(serviceSpec.Deployment.SecretEnvironmentVars) > 0 {
					for _, secretVar := range serviceSpec.Deployment.SecretEnvironmentVars {
						p.logger.Info().Str("service", serviceName).Str("secret", secretVar.ValueFrom).Msg("Inferred secret accessor role")
						binding := IAMBinding{ResourceType: "secret", ResourceID: secretVar.ValueFrom, Role: "roles/secretmanager.secretAccessor"}
						addBindingToPlan(serviceSpec.ServiceAccount, binding, finalPlan, &mu)
					}
				}
			}

			// Infer Cloud Run Invoker role from dependencies
			if len(serviceSpec.Dependencies) > 0 {
				for _, dependencyName := range serviceSpec.Dependencies {
					p.logger.Info().Str("service", serviceName).Str("dependency", dependencyName).Msg("Inferred cloud run invoker role")
					dependencyRegion := findServiceRegion(arch, dependencyName)
					binding := IAMBinding{
						ResourceType:     "cloudrun_service",
						ResourceID:       dependencyName,
						Role:             "roles/run.invoker",
						ResourceLocation: dependencyRegion,
					}
					addBindingToPlan(serviceSpec.ServiceAccount, binding, finalPlan, &mu)
				}
			}
			scanResourcesForExplicitPolicies(serviceName, serviceSpec.ServiceAccount, dataflow.Resources, finalPlan, &mu)
		}
	}

	p.logger.Info().Int("services_planned", len(finalPlan)).Msg("Application service IAM role plan complete.")
	return finalPlan, nil
}

// REFACTOR: This new helper function encapsulates the explicit Pub/Sub role planning.
func (p *RolePlanner) planPubSubRolesFromResources(dataflow servicemanager.ResourceGroup, plan map[string][]IAMBinding, mu *sync.Mutex) {
	// Plan Publisher roles from Topics
	for _, topic := range dataflow.Resources.Topics {
		if topic.ProducerService != "" {
			if service, ok := dataflow.Services[topic.ProducerService]; ok {
				p.logger.Info().Str("service", service.Name).Str("topic", topic.Name).Msg("Planning explicit publisher and viewer roles")
				// Publisher role on the topic
				pubBinding := IAMBinding{ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: "roles/pubsub.publisher"}
				addBindingToPlan(service.ServiceAccount, pubBinding, plan, mu)
				// Viewer role on the topic
				viewBinding := IAMBinding{ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: "roles/pubsub.viewer"}
				addBindingToPlan(service.ServiceAccount, viewBinding, plan, mu)
			}
		}
	}

	// Plan Subscriber roles from Subscriptions
	for _, sub := range dataflow.Resources.Subscriptions {
		if sub.ConsumerService != "" {
			if service, ok := dataflow.Services[sub.ConsumerService]; ok {
				p.logger.Info().Str("service", service.Name).Str("subscription", sub.Name).Msg("Planning explicit subscriber and topic viewer roles")
				// Subscriber role on the subscription
				subBinding := IAMBinding{ResourceType: "pubsub_subscription", ResourceID: sub.Name, Role: "roles/pubsub.subscriber"}
				addBindingToPlan(service.ServiceAccount, subBinding, plan, mu)
				// Viewer role on the subscription's TOPIC
				viewBinding := IAMBinding{ResourceType: "pubsub_topic", ResourceID: sub.Topic, Role: "roles/pubsub.viewer"}
				addBindingToPlan(service.ServiceAccount, viewBinding, plan, mu)
			}
		}
	}
}

// scanResourcesForExplicitPolicies is a helper that checks all resource types for explicit IAM policies.
func scanResourcesForExplicitPolicies(serviceName, serviceAccount string, resources servicemanager.CloudResourcesSpec, plan map[string][]IAMBinding, mu *sync.Mutex) {
	// Check Topics
	for _, topic := range resources.Topics {
		for _, policy := range topic.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: policy.Role}
				addBindingToPlan(serviceAccount, binding, plan, mu)
			}
		}
	}
	// Check Subscriptions
	for _, sub := range resources.Subscriptions {
		for _, policy := range sub.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{ResourceType: "pubsub_subscription", ResourceID: sub.Name, Role: policy.Role}
				addBindingToPlan(serviceAccount, binding, plan, mu)
			}
		}
	}
	// Check GCS Buckets
	for _, bucket := range resources.GCSBuckets {
		for _, policy := range bucket.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{ResourceType: "gcs_bucket", ResourceID: bucket.Name, Role: policy.Role}
				addBindingToPlan(serviceAccount, binding, plan, mu)
			}
		}
	}
	// Check BigQuery Datasets
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
	// Check the service director first
	if arch.ServiceManagerSpec.Name == serviceName && arch.ServiceManagerSpec.Deployment != nil {
		if arch.ServiceManagerSpec.Deployment.Region != "" {
			return arch.ServiceManagerSpec.Deployment.Region
		}
	}
	// Check all dataflow services
	for _, df := range arch.Dataflows {
		if svc, ok := df.Services[serviceName]; ok && svc.Deployment != nil {
			if svc.Deployment.Region != "" {
				return svc.Deployment.Region
			}
		}
	}
	// Fallback to the global default region
	return arch.Environment.Region
}
