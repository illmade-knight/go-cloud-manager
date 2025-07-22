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

// PlanRolesForApplicationServices now infers common roles from environment variables
// for Pub/Sub, BigQuery, and GCS, in addition to reading explicit policies.
func (p *RolePlanner) PlanRolesForApplicationServices(ctx context.Context, arch *servicemanager.MicroserviceArchitecture) (map[string][]IAMBinding, error) {
	p.logger.Info().Str("architecture", arch.Name).Msg("Planning required IAM roles for all application services...")

	finalPlan := make(map[string][]IAMBinding)
	var mu sync.Mutex

	for _, dataflow := range arch.Dataflows {
		for serviceName, serviceSpec := range dataflow.Services {
			// --- Role Inference Logic ---
			if serviceSpec.Deployment != nil && serviceSpec.Deployment.EnvironmentVars != nil {
				envVars := serviceSpec.Deployment.EnvironmentVars
				// Infer Pub/Sub Publisher role
				if topicID, ok := envVars["TOPIC_ID"]; ok {
					p.logger.Info().Str("service", serviceName).Str("topic", topicID).Msg("Inferred publisher role")
					addBindingToPlan(serviceSpec.ServiceAccount, "pubsub_topic", topicID, "roles/pubsub.publisher", finalPlan, &mu)
				}
				// Infer Pub/Sub Subscriber role
				if subID, ok := envVars["SUBSCRIPTION_ID"]; ok {
					p.logger.Info().Str("service", serviceName).Str("subscription", subID).Msg("Inferred subscriber role")
					addBindingToPlan(serviceSpec.ServiceAccount, "pubsub_subscription", subID, "roles/pubsub.subscriber", finalPlan, &mu)
				}
				// Infer BigQuery Data Editor role
				if datasetID, ok := envVars["BIGQUERY_DATASET_ID"]; ok {
					p.logger.Info().Str("service", serviceName).Str("dataset", datasetID).Msg("Inferred bigquery data editor role")
					addBindingToPlan(serviceSpec.ServiceAccount, "bigquery_dataset", datasetID, "roles/bigquery.dataEditor", finalPlan, &mu)
				}
				// Infer GCS Object Admin role
				if bucketName, ok := envVars["GCS_BUCKET_NAME"]; ok {
					p.logger.Info().Str("service", serviceName).Str("bucket", bucketName).Msg("Inferred storage object admin role")
					addBindingToPlan(serviceSpec.ServiceAccount, "gcs_bucket", bucketName, "roles/storage.objectAdmin", finalPlan, &mu)
				}
			}

			// --- Scan for explicit policies ---
			scanResourcesForExplicitPolicies(serviceName, serviceSpec.ServiceAccount, dataflow.Resources, finalPlan, &mu)
		}
	}

	p.logger.Info().Int("services_planned", len(finalPlan)).Msg("Application service IAM role plan complete.")
	return finalPlan, nil
}

// scanResourcesForExplicitPolicies is a helper that checks all resource types for explicit IAM policies.
func scanResourcesForExplicitPolicies(serviceName, serviceAccount string, resources servicemanager.CloudResourcesSpec, plan map[string][]IAMBinding, mu *sync.Mutex) {
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
