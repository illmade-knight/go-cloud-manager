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

// PlanRolesForServiceDirector remains unchanged.
func (p *RolePlanner) PlanRolesForServiceDirector(arch *servicemanager.MicroserviceArchitecture) ([]string, error) {
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

		// If any dataflow defines application services, the ServiceDirector
		// will be responsible for ensuring their service accounts exist. Therefore,
		// it needs permission to create and manage service accounts.
		if len(dataflow.Services) > 0 {
			requiredRoles["roles/iam.serviceAccountAdmin"] = struct{}{}
		}

		for _, service := range dataflow.Services {
			if service.Deployment != nil && len(service.Deployment.SecretEnvironmentVars) > 0 {
				requiredRoles["roles/secretmanager.admin"] = struct{}{}
				break
			}

			if len(service.Dependencies) > 0 {
				requiredRoles["roles/run.admin"] = struct{}{}
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

// PlanRolesForApplicationServices This function returns a flat slice of all bindings
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

// planDataResourceLinkRoles: All planning helpers add the ServiceAccount to the binding and call the addBindingToPlan helper.
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
}

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

func scanResourcesForExplicitPolicies(serviceName, serviceAccount string, resources servicemanager.CloudResourcesSpec, plan *[]IAMBinding, mu *sync.Mutex) {
	for _, topic := range resources.Topics {
		for _, policy := range topic.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{ServiceAccount: serviceAccount, ResourceType: "pubsub_topic", ResourceID: topic.Name, Role: policy.Role}
				addBindingToPlan(binding, plan, mu)
			}
		}
	}
	// ... (repeat for other resource types) ...
}

// REFACTOR: This helper is updated to append to a slice pointer.
func addBindingToPlan(binding IAMBinding, plan *[]IAMBinding, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	*plan = append(*plan, binding)
}

func findServiceRegion(arch *servicemanager.MicroserviceArchitecture, serviceName string) string {
	// ... (implementation is unchanged) ...
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
