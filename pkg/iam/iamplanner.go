package iam

import (
	"context"
	"sync"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMBinding represents a single "who gets what on where" permission.
type IAMBinding struct {
	ResourceType string
	ResourceID   string
	Role         string
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
func (p *RolePlanner) PlanRolesForServiceDirector(ctx context.Context, arch *servicemanager.MicroserviceArchitecture) ([]string, error) {
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
	}

	rolesSlice := make([]string, 0, len(requiredRoles))
	for role := range requiredRoles {
		rolesSlice = append(rolesSlice, role)
	}

	p.logger.Info().Strs("roles", rolesSlice).Msg("ServiceDirector IAM role plan complete.")
	return rolesSlice, nil
}

// PlanRolesForApplicationServices inspects the architecture and creates a detailed plan of which
// roles each individual application service needs on each specific resource.
func (p *RolePlanner) PlanRolesForApplicationServices(ctx context.Context, arch *servicemanager.MicroserviceArchitecture) (map[string][]IAMBinding, error) {
	p.logger.Info().Str("architecture", arch.Name).Msg("Planning required IAM roles for all application services...")

	// The final plan will be a map where the key is the service account name (e.g., "ingestion-sa")
	// and the value is a list of all the permissions it needs.
	finalPlan := make(map[string][]IAMBinding)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errChan := make(chan error, len(arch.Dataflows))

	// As you suggested, we can process each dataflow in parallel.
	for _, dataflow := range arch.Dataflows {
		wg.Add(1)
		go func(df servicemanager.ResourceGroup) {
			defer wg.Done()
			// For each service in this dataflow...
			for serviceName, serviceSpec := range df.Services {
				// ...scan all resources in this dataflow to find policies for this service.
				scanResourcesForService(serviceName, serviceSpec.ServiceAccount, df.Resources, finalPlan, &mu)
			}
		}(dataflow)
	}

	wg.Wait()
	close(errChan)
	// In a real implementation, you might want to handle errors from the goroutines.

	p.logger.Info().Int("services_planned", len(finalPlan)).Msg("Application service IAM role plan complete.")
	return finalPlan, nil
}

// scanResourcesForService is a helper that checks all resource types for IAM policies matching a given service.
func scanResourcesForService(serviceName, serviceAccount string, resources servicemanager.CloudResourcesSpec, plan map[string][]IAMBinding, mu *sync.Mutex) {
	// Check Topics
	for _, topic := range resources.Topics {
		for _, policy := range topic.IAMPolicy {
			if policy.Name == serviceName {
				addBindingToPlan(serviceAccount, "pubsub_topic", topic.Name, policy.Role, plan, mu)
			}
		}
	}
	// Check Subscriptions
	for _, sub := range resources.Subscriptions {
		for _, policy := range sub.IAMPolicy {
			if policy.Name == serviceName {
				addBindingToPlan(serviceAccount, "pubsub_subscription", sub.Name, policy.Role, plan, mu)
			}
		}
	}
	// Check GCS Buckets
	for _, bucket := range resources.GCSBuckets {
		for _, policy := range bucket.IAMPolicy {
			if policy.Name == serviceName {
				addBindingToPlan(serviceAccount, "gcs_bucket", bucket.Name, policy.Role, plan, mu)
			}
		}
	}
	// Check BigQuery Datasets
	for _, dataset := range resources.BigQueryDatasets {
		for _, policy := range dataset.IAMPolicy {
			if policy.Name == serviceName {
				addBindingToPlan(serviceAccount, "bigquery_dataset", dataset.Name, policy.Role, plan, mu)
			}
		}
	}
}

// addBindingToPlan is a thread-safe helper to add a new binding to our final plan.
func addBindingToPlan(serviceAccount, resourceType, resourceID, role string, plan map[string][]IAMBinding, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	plan[serviceAccount] = append(plan[serviceAccount], IAMBinding{
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Role:         role,
	})
}
