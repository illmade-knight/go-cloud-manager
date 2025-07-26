package iam

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMManager defines the high-level orchestration logic for applying IAM policies.
type IAMManager interface {
	ApplyIAMForService(ctx context.Context, dataflow servicemanager.ResourceGroup, serviceName string) error
}

type simpleIAMManager struct {
	client IAMClient
	logger zerolog.Logger
}

func NewIAMManager(client IAMClient, logger zerolog.Logger) IAMManager {
	return &simpleIAMManager{
		client: client,
		logger: logger.With().Str("component", "IAMManager").Logger(),
	}
}

// ApplyIAMForService handles all IAM policies for a single service within its dataflow.
func (im *simpleIAMManager) ApplyIAMForService(ctx context.Context, dataflow servicemanager.ResourceGroup, serviceName string) error {
	im.logger.Info().Str("service", serviceName).Str("dataflow", dataflow.Name).Msg("Applying IAM policies...")

	// 1. Find the service's defined service account name.
	serviceSpec, ok := dataflow.Services[serviceName]
	if !ok {
		return fmt.Errorf("service '%s' not found in dataflow '%s'", serviceName, dataflow.Name)
	}
	serviceAccountName := serviceSpec.ServiceAccount

	// 2. Ensure the service account exists.
	saEmail, err := im.client.EnsureServiceAccountExists(ctx, serviceAccountName)
	if err != nil {
		return fmt.Errorf("failed to ensure service account '%s' exists: %w", serviceAccountName, err)
	}

	member := "serviceAccount:" + saEmail

	// 3. Iterate through all resources within this dataflow and apply policies.
	resources := dataflow.Resources

	// Handle Pub/Sub Topics
	for _, topic := range resources.Topics {
		for _, policy := range topic.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{
					ResourceType: "pubsub_topic",
					ResourceID:   topic.Name,
					Role:         policy.Role,
				}
				err := im.client.AddResourceIAMBinding(ctx, binding, member)
				if err != nil {
					return err
				}
			}
		}
	}

	// Handle Pub/Sub Subscriptions
	for _, sub := range resources.Subscriptions {
		for _, policy := range sub.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{
					ResourceType: "pubsub_subscription",
					ResourceID:   sub.Name,
					Role:         policy.Role,
				}
				err := im.client.AddResourceIAMBinding(ctx, binding, member)
				if err != nil {
					return err
				}
			}
		}
	}

	// Handle GCS Buckets
	for _, bucket := range resources.GCSBuckets {
		for _, policy := range bucket.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{
					ResourceType: "gcs_bucket",
					ResourceID:   bucket.Name,
					Role:         policy.Role,
				}
				err := im.client.AddResourceIAMBinding(ctx, binding, member)
				if err != nil {
					return err
				}
			}
		}
	}

	// Handle BigQuery Datasets
	for _, dataset := range resources.BigQueryDatasets {
		for _, policy := range dataset.IAMPolicy {
			if policy.Name == serviceName {
				binding := IAMBinding{
					ResourceType: "bigquery_dataset",
					ResourceID:   dataset.Name,
					Role:         policy.Role,
				}
				err := im.client.AddResourceIAMBinding(ctx, binding, member)
				if err != nil {
					return err
				}
			}
		}
	}

	if serviceSpec.Deployment != nil && len(serviceSpec.Deployment.SecretEnvironmentVars) > 0 {
		for _, secretVar := range serviceSpec.Deployment.SecretEnvironmentVars {
			im.logger.Info().Str("secret", secretVar.ValueFrom).Msg("Applying secret accessor role...")
			binding := IAMBinding{
				ResourceType: "secret",
				ResourceID:   secretVar.ValueFrom,
				Role:         "roles/secretmanager.secretAccessor",
			}
			err := im.client.AddResourceIAMBinding(ctx, binding, member)
			if err != nil {
				return fmt.Errorf("failed to grant secret access for '%s' to service '%s': %w", secretVar.ValueFrom, serviceName, err)
			}
		}
	}

	im.logger.Info().Str("service", serviceName).Msg("Successfully applied all IAM policies.")
	return nil
}
