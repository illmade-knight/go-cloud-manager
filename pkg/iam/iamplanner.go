package iam

import (
	"fmt"
	"sync"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMBinding struct for creating a single IAM binding linked to a service account.
type IAMBinding struct {
	ServiceAccount   string
	ResourceType     string
	ResourceID       string
	Role             string
	ResourceLocation string
}

// PolicyBinding represents the desired state of all IAM roles for a single resource.
type PolicyBinding struct {
	ResourceType     string
	ResourceID       string
	ResourceLocation string
	MemberRoles      map[string][]string
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

// PlanRolesForServiceDirector plans the necessary administrative roles for the orchestrator.
func (p *RolePlanner) PlanRolesForServiceDirector(arch *servicemanager.MicroserviceArchitecture) ([]IAMBinding, error) {
	p.logger.Info().Str("architecture", arch.Environment.Name).Msg("Planning required IAM roles for ServiceDirector...")

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
		if len(resources.FirestoreDatabases) > 0 {
			// NOTE: The 'datastore.owner' role is required for the orchestrator to be
			// able to create the Firestore database instance itself.
			requiredRoles["roles/datastore.owner"] = struct{}{}
		}
		if len(dataflow.Services) > 0 {
			requiredRoles["roles/iam.serviceAccountAdmin"] = struct{}{}
		}
		for _, service := range dataflow.Services {
			if service.Deployment != nil && len(service.Deployment.SecretEnvironmentVars) > 0 {
				requiredRoles["roles/secretmanager.admin"] = struct{}{}
			}
			if len(service.Dependencies) > 0 {
				requiredRoles["roles/run.admin"] = struct{}{}
			}
		}
	}

	// Convert the map of roles into a structured slice of IAMBinding structs.
	bindings := make([]IAMBinding, 0, len(requiredRoles))
	for role := range requiredRoles {
		bindings = append(bindings, IAMBinding{
			ServiceAccount: arch.ServiceManagerSpec.ServiceAccount,
			ResourceType:   "project",
			ResourceID:     arch.ProjectID,
			Role:           role,
		})
	}

	p.logger.Info().Int("bindings_planned", len(bindings)).Msg("ServiceDirector IAM role plan complete.")
	return bindings, nil
}

// PlanRolesForApplicationServices returns a flat slice of all bindings for application services.
func (p *RolePlanner) PlanRolesForApplicationServices(arch *servicemanager.MicroserviceArchitecture) ([]IAMBinding, error) {
	p.logger.Info().Str("architecture", arch.Environment.Name).Msg("Planning required IAM roles for all application services...")

	var finalPlan []IAMBinding
	var mu sync.Mutex

	for _, dataflow := range arch.Dataflows {
		p.planPubSubLinkRoles(dataflow, &finalPlan, &mu)
		p.planDataResourceLinkRoles(dataflow, &finalPlan, &mu)

		for serviceName, serviceSpec := range dataflow.Services {
			p.planDependencyRoles(arch, serviceName, serviceSpec, &finalPlan, &mu)
			p.planResourceUsageRoles(serviceName, serviceSpec, dataflow.Resources, &finalPlan, &mu)
		}
	}

	p.logger.Info().Int("bindings_planned", len(finalPlan)).Msg("Application service IAM role plan complete.")
	return finalPlan, nil
}

// planDataResourceLinkRoles plans roles based on producer/consumer links for data resources.
func (p *RolePlanner) planDataResourceLinkRoles(dataflow servicemanager.ResourceGroup, plan *[]IAMBinding, mu *sync.Mutex) {
	for _, bucket := range dataflow.Resources.GCSBuckets {
		for _, producer := range bucket.Producers {
			if service, ok := dataflow.Services[producer.Name]; ok {
				binding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "gcs_bucket", ResourceID: bucket.Name, Role: "roles/storage.objectAdmin"}
				addBindingToPlan(binding, plan, mu)
			}
		}
		for _, consumer := range bucket.Consumers {
			if service, ok := dataflow.Services[consumer.Name]; ok {
				binding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "gcs_bucket", ResourceID: bucket.Name, Role: "roles/storage.objectViewer"}
				addBindingToPlan(binding, plan, mu)
			}
		}
	}

	for _, table := range dataflow.Resources.BigQueryTables {
		resourceID := fmt.Sprintf("%s:%s", table.Dataset, table.Name)
		for _, producer := range table.Producers {
			if service, ok := dataflow.Services[producer.Name]; ok {
				binding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "bigquery_table", ResourceID: resourceID, Role: "roles/bigquery.dataEditor"}
				addBindingToPlan(binding, plan, mu)
			}
		}
		for _, consumer := range table.Consumers {
			if service, ok := dataflow.Services[consumer.Name]; ok {
				binding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "bigquery_table", ResourceID: resourceID, Role: "roles/bigquery.dataViewer"}
				addBindingToPlan(binding, plan, mu)
			}
		}
	}

	// UPDATE: This new block adds IAM planning for Firestore. It reads the
	// producer/consumer fields and creates the appropriate role bindings.
	for _, db := range dataflow.Resources.FirestoreDatabases {
		// NOTE: Unlike other resources, Firestore roles are granted at the project
		// level, so the ResourceID for the binding is not used in the same way,
		// but we set it for consistency in the plan.
		//const firestoreResourceID = "(default)"
		for _, producer := range db.Producers {
			if service, ok := dataflow.Services[producer.Name]; ok {
				// The 'datastore.user' role grants read and write permissions.
				binding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "project",
					ResourceID: dataflow.Services[producer.Name].Name, Role: "roles/datastore.user"}

				addBindingToPlan(binding, plan, mu)
			}
		}
		for _, consumer := range db.Consumers {
			if service, ok := dataflow.Services[consumer.Name]; ok {
				// The 'datastore.viewer' role grants read-only permissions.
				binding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "project", ResourceID: dataflow.Services[consumer.Name].Name, Role: "roles/datastore.viewer"}
				addBindingToPlan(binding, plan, mu)
			}
		}
	}
}

// planPubSubLinkRoles plans roles based on producer/consumer links for messaging.
func (p *RolePlanner) planPubSubLinkRoles(dataflow servicemanager.ResourceGroup, plan *[]IAMBinding, mu *sync.Mutex) {
	for _, topic := range dataflow.Resources.Topics {
		if topic.ProducerService != nil {
			if service, ok := dataflow.Services[topic.ProducerService.Name]; ok {
				pubBinding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: "roles/pubsub.publisher"}
				viewBinding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: "roles/pubsub.viewer"}
				addBindingToPlan(pubBinding, plan, mu)
				addBindingToPlan(viewBinding, plan, mu)
			}
		}
	}

	for _, sub := range dataflow.Resources.Subscriptions {
		if sub.ConsumerService != nil {
			if service, ok := dataflow.Services[sub.ConsumerService.Name]; ok {
				subBinding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "pubsub_subscription", ResourceID: sub.Name, Role: "roles/pubsub.subscriber"}
				viewSubBinding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "pubsub_subscription", ResourceID: sub.Name, Role: "roles/pubsub.viewer"}
				viewTopicBinding := IAMBinding{ServiceAccount: service.ServiceAccount, ResourceType: "pubsub_topic", ResourceID: sub.Topic, Role: "roles/pubsub.viewer"}
				addBindingToPlan(subBinding, plan, mu)
				addBindingToPlan(viewSubBinding, plan, mu)
				addBindingToPlan(viewTopicBinding, plan, mu)
			}
		}
	}
}

// planDependencyRoles plans the 'run.invoker' role for service-to-service communication.
func (p *RolePlanner) planDependencyRoles(arch *servicemanager.MicroserviceArchitecture, serviceName string, serviceSpec servicemanager.ServiceSpec, plan *[]IAMBinding, mu *sync.Mutex) {
	if len(serviceSpec.Dependencies) > 0 {
		for _, dependencyName := range serviceSpec.Dependencies {
			dependencyRegion := findServiceRegion(arch, dependencyName)
			binding := IAMBinding{
				ServiceAccount:   serviceSpec.ServiceAccount,
				ResourceType:     "cloudrun_service",
				ResourceID:       dependencyName,
				Role:             "roles/run.invoker",
				ResourceLocation: dependencyRegion,
			}
			addBindingToPlan(binding, plan, mu)
		}
	}
}

// planResourceUsageRoles plans roles for secrets and from explicit IAM policies.
func (p *RolePlanner) planResourceUsageRoles(serviceName string, serviceSpec servicemanager.ServiceSpec, resources servicemanager.CloudResourcesSpec, plan *[]IAMBinding, mu *sync.Mutex) {
	scanResourcesForExplicitPolicies(serviceName, serviceSpec.ServiceAccount, resources, plan, mu)

	if serviceSpec.Deployment != nil {
		if len(serviceSpec.Deployment.SecretEnvironmentVars) > 0 {
			for _, secretVar := range serviceSpec.Deployment.SecretEnvironmentVars {
				binding := IAMBinding{ServiceAccount: serviceSpec.ServiceAccount, ResourceType: "secret", ResourceID: secretVar.ValueFrom, Role: "roles/secretmanager.secretAccessor"}
				addBindingToPlan(binding, plan, mu)
			}
		}
	}
}

// scanResourcesForExplicitPolicies processes the 'iam_access_policy' fields in the YAML.
func scanResourcesForExplicitPolicies(serviceName, serviceAccount string, resources servicemanager.CloudResourcesSpec, plan *[]IAMBinding, mu *sync.Mutex) {
	// Helper to reduce repetition
	addPolicyToPlan := func(resourceType, resourceID, role string) {
		binding := IAMBinding{ServiceAccount: serviceAccount, ResourceType: resourceType, ResourceID: resourceID, Role: role}
		addBindingToPlan(binding, plan, mu)
	}

	for _, topic := range resources.Topics {
		for _, policy := range topic.IAMPolicy {
			if policy.Name == serviceName {
				addPolicyToPlan("pubsub_topic", topic.Name, policy.Role)
			}
		}
	}

	for _, sub := range resources.Subscriptions {
		for _, policy := range sub.IAMPolicy {
			if policy.Name == serviceName {
				addPolicyToPlan("pubsub_subscription", sub.Name, policy.Role)
			}
		}
	}
	for _, bucket := range resources.GCSBuckets {
		for _, policy := range bucket.IAMPolicy {
			if policy.Name == serviceName {
				addPolicyToPlan("gcs_bucket", bucket.Name, policy.Role)
			}
		}
	}
	for _, dataset := range resources.BigQueryDatasets {
		for _, policy := range dataset.IAMPolicy {
			if policy.Name == serviceName {
				addPolicyToPlan("bigquery_dataset", dataset.Name, policy.Role)
			}
		}
	}
	for _, db := range resources.FirestoreDatabases {
		for _, policy := range db.IAMPolicy {
			if policy.Name == serviceName {
				// Firestore IAM roles are project-level.
				addPolicyToPlan("project", serviceName, policy.Role)
			}
		}
	}
}

func addBindingToPlan(binding IAMBinding, plan *[]IAMBinding, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	*plan = append(*plan, binding)
}

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
