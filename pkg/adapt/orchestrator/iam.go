package orchestrator

import (
	"context"
	"fmt"
	"github.com/illmade-knight/go-iot-dataflows/deployment/iam"
	"github.com/illmade-knight/go-iot/pkg/servicemanager"
	"github.com/rs/zerolog"
)

type IAMProvisioner struct {
	iamClient *iam.Client
	logger    zerolog.Logger
}

// ProvisionIAM provisions necessary IAM service accounts and bindings based on the services definition.
func (p *IAMProvisioner) provisionIAM(ctx context.Context, servicesConfig *servicemanager.TopLevelConfig) error {
	p.logger.Info().Msg("Starting IAM provisioning for dataflow resources.")

	projectID := servicesConfig.DefaultProjectID
	p.logger.Info().Str("projectID", projectID).Msg("provisioning IAM")
	// Iterate through dataflows to provision IAM for their resources
	for _, dataflow := range servicesConfig.Dataflows { // dataflow is of type servicemanager.ResourceGroup
		p.logger.Info().Str("dataflow", dataflow.Name).Msg("Processing IAM for dataflow resources.")

		err := p.setupBigquery(ctx, projectID, dataflow.Resources.BigQueryDatasets)
		if err != nil {
			return err
		}
		err = p.setupBuckets(ctx, projectID, dataflow.Resources.GCSBuckets)
		if err != nil {
			return err
		}
		err = p.setupTopics(ctx, projectID, dataflow.Resources.Topics)
		if err != nil {
			return err
		}
	}
	p.logger.Info().Msg("IAM provisioning completed successfully.")
	return nil
}

func (p *IAMProvisioner) TeardownIAM(ctx context.Context, projectID string, dataflow servicemanager.ResourceGroup) error {
	p.logger.Info().Msg("Starting IAM provisioning for dataflow resources.")
	p.logger.Info().Str("dataflow", dataflow.Name).Msg("Processing IAM for dataflow resources.")

	p.teardownTopics(ctx, projectID, dataflow.Resources.Topics)
	err := p.teardownBuckets(ctx, projectID, dataflow.Resources.GCSBuckets)
	if err != nil {
		return err
	}
	err = p.teardownBigquery(ctx, projectID, dataflow.Resources.BigQueryDatasets)
	if err != nil {
		return err
	}

	return nil
}

func (ps *IAMProvisioner) setupBigquery(ctx context.Context, projectID string, datasets []servicemanager.BigQueryDataset) error {
	for _, dataset := range datasets {
		if dataset.IAMAccessPolicy.IAMAccess == nil {
			ps.logger.Debug().Str("dataset", dataset.Name).Msg("No IAM access policies defined for dataset, skipping.")
			continue
		}
		for _, iamPolicy := range dataset.IAMAccessPolicy.IAMAccess {
			// Assuming iamPolicy.Name directly provides the service account ID/name
			serviceAccount := iamPolicy.Name
			serviceAccountEmail, err := ps.iamClient.EnsureServiceAccountExists(ctx, projectID, serviceAccount)
			if err != nil {
				return fmt.Errorf("failed to ensure service account %s for BigQuery dataset %s: %w", serviceAccount, dataset.Name, err)
			}
			// Note: AddBigQueryDatasetIAMBinding usually needs project ID for dataset context
			if err := ps.iamClient.AddBigQueryDatasetIAMBinding(ctx, projectID, dataset.Name, iamPolicy.Role, serviceAccountEmail); err != nil {
				return fmt.Errorf("failed to add IAM binding for BigQuery dataset %s: %w", dataset.Name, err)
			}
			ps.logger.Info().Str("dataset", dataset.Name).Str("role", iamPolicy.Role).Msg("Applied IAM binding for BigQuery dataset.")
		}
	}
	return nil
}

func (ps *IAMProvisioner) teardownBigquery(ctx context.Context, projectID string, datasets []servicemanager.BigQueryDataset) error {
	for _, dataset := range datasets {
		if dataset.IAMAccessPolicy.IAMAccess == nil {
			ps.logger.Debug().Str("dataset", dataset.Name).Msg("No IAM access policies defined for dataset, skipping teardown.")
			continue
		}
		for _, iamPolicy := range dataset.IAMAccessPolicy.IAMAccess {
			serviceAccount := iamPolicy.Name
			serviceAccountEmail, err := ps.iamClient.EnsureServiceAccountExists(ctx, projectID, serviceAccount)
			if err != nil {
				ps.logger.Warn().Err(err).Str("service_account", serviceAccount).Msg("Could not resolve service account email for IAM teardown. Attempting removal with provided SA name.")
				serviceAccountEmail = fmt.Sprintf("%s@%s.iam.gserviceaccount.com", serviceAccount, projectID) // Fallback to assumed email format
			}
			ps.logger.Info().Str("dataset", dataset.Name).Str("role", iamPolicy.Role).Str("service_account_email", serviceAccountEmail).Msg("Attempting to remove IAM binding for BigQuery dataset.")
			if err := ps.iamClient.RemoveBigQueryDatasetIAMBinding(ctx, projectID, dataset.Name, iamPolicy.Role, serviceAccountEmail); err != nil {
				ps.logger.Error().Err(err).Msg("Failed to remove IAM binding for BigQuery dataset. Continuing...")
			}
		}
	}
	return nil
}

func (ps *IAMProvisioner) setupBuckets(ctx context.Context, projectID string, buckets []servicemanager.GCSBucket) error {
	for _, bucket := range buckets {
		if bucket.IAMAccessPolicy.IAMAccess == nil {
			ps.logger.Debug().Str("bucket", bucket.Name).Msg("No IAM access policies defined for bucket, skipping.")
			continue
		}
		for _, iamPolicy := range bucket.IAMAccessPolicy.IAMAccess {
			// Assuming iamPolicy.Name directly provides the service account ID/name
			serviceAccount := iamPolicy.Name
			serviceAccountEmail, err := ps.iamClient.EnsureServiceAccountExists(ctx, projectID, serviceAccount)
			if err != nil {
				return fmt.Errorf("failed to ensure service account %s for GCS bucket %s: %w", serviceAccount, bucket.Name, err)
			}
			if err := ps.iamClient.AddGCSBucketIAMBinding(ctx, bucket.Name, iamPolicy.Role, serviceAccountEmail); err != nil {
				return fmt.Errorf("failed to add IAM binding for GCS bucket %s: %w", bucket.Name, err)
			}
			ps.logger.Info().Str("bucket", bucket.Name).Str("role", iamPolicy.Role).Msg("Applied IAM binding for GCS bucket.")
		}
	}
	return nil
}

func (ps *IAMProvisioner) teardownBuckets(ctx context.Context, projectID string, buckets []servicemanager.GCSBucket) error {
	for _, bucket := range buckets {
		if bucket.IAMAccessPolicy.IAMAccess == nil {
			ps.logger.Debug().Str("bucket", bucket.Name).Msg("No IAM access policies defined for bucket, skipping teardown.")
			continue
		}
		for _, iamPolicy := range bucket.IAMAccessPolicy.IAMAccess {
			serviceAccount := iamPolicy.Name
			serviceAccountEmail, err := ps.iamClient.EnsureServiceAccountExists(ctx, projectID, serviceAccount)
			if err != nil {
				ps.logger.Warn().Err(err).Str("service_account", serviceAccount).Msg("Could not resolve service account email for IAM teardown. Attempting removal with provided SA name.")
				serviceAccountEmail = fmt.Sprintf("%s@%s.iam.gserviceaccount.com", serviceAccount, projectID) // Fallback to assumed email format
			}
			ps.logger.Info().Str("bucket", bucket.Name).Str("role", iamPolicy.Role).Str("service_account_email", serviceAccountEmail).Msg("Attempting to remove IAM binding for GCS bucket.")
			if err := ps.iamClient.RemoveGCSBucketIAMBinding(ctx, bucket.Name, iamPolicy.Role, serviceAccountEmail); err != nil {
				ps.logger.Error().Err(err).Msg("Failed to remove IAM binding for GCS bucket. Continuing...")
			}
		}
	}
	return nil
}

func (ps *IAMProvisioner) setupTopics(ctx context.Context, projectID string, topics []servicemanager.TopicConfig) error {
	for _, topic := range topics { // Correctly using .Topics as per types.go
		if topic.IAMAccessPolicy.IAMAccess == nil {
			ps.logger.Debug().Str("topic", topic.Name).Msg("No IAM access policies defined for topic, skipping.")
			continue
		}
		for _, iamPolicy := range topic.IAMAccessPolicy.IAMAccess {
			// Assuming iamPolicy.Name directly provides the service account ID/name
			// Corrected: use iamPolicy.Name directly as the service account.
			serviceAccount := iamPolicy.Name
			serviceAccountEmail, err := ps.iamClient.EnsureServiceAccountExists(ctx, projectID, serviceAccount)
			if err != nil {
				return fmt.Errorf("failed to ensure service account %s for Pub/Sub topic %s: %w", serviceAccount, topic.Name, err)
			}
			if err := ps.iamClient.AddPubSubTopicIAMBinding(ctx, topic.Name, iamPolicy.Role, serviceAccountEmail); err != nil {
				return fmt.Errorf("failed to add IAM binding for Pub/Sub topic %s: %w", topic.Name, err)
			}
			ps.logger.Info().Str("topic", topic.Name).Str("role", iamPolicy.Role).Msg("Applied IAM binding for Pub/Sub topic.")
		}
	}
	return nil
}

func (ps *IAMProvisioner) teardownTopics(ctx context.Context, projectID string, topics []servicemanager.TopicConfig) {
	for _, topic := range topics { // Correctly using .Topics as per types.go
		if topic.IAMAccessPolicy.IAMAccess == nil {
			ps.logger.Debug().Str("topic", topic.Name).Msg("No IAM access policies defined for topic, skipping teardown.")
			continue
		}
		for _, iamPolicy := range topic.IAMAccessPolicy.IAMAccess {
			// Assuming iamPolicy.Name directly provides the service account email or ID
			serviceAccount := iamPolicy.Name
			// Ensure we have the full email for removal, even if the service account might have been deleted already.
			// This call might fail if the SA is gone, but we proceed to attempt binding removal.
			serviceAccountEmail, err := ps.iamClient.EnsureServiceAccountExists(ctx, projectID, serviceAccount)
			if err != nil {
				ps.logger.Warn().Err(err).Str("service_account", serviceAccount).Msg("Could not resolve service account email for IAM teardown. Attempting removal with provided SA name.")
				serviceAccountEmail = fmt.Sprintf("%s@%s.iam.gserviceaccount.com", serviceAccount, projectID) // Fallback to assumed email format
			}

			ps.logger.Info().Str("topic", topic.Name).Str("role", iamPolicy.Role).Str("service_account_email", serviceAccountEmail).Msg("Attempting to remove IAM binding for Pub/Sub topic.")
			// This assumes an `iamClient.RemovePubSubTopicIAMBinding` exists.
			// If not, this is a logical placeholder that needs implementation in the IAM client.
			if err := ps.iamClient.RemovePubSubTopicIAMBinding(ctx, topic.Name, iamPolicy.Role, serviceAccountEmail); err != nil {
				ps.logger.Error().Err(err).Msg("Failed to remove IAM binding for Pub/Sub topic. Continuing...")
			}
		}
	}
}

func (ps *IAMProvisioner) setupSubscriptions(ctx context.Context, projectID string, subscriptions []servicemanager.SubscriptionConfig) error {
	for _, subscription := range subscriptions { // Correctly using .Topics as per types.go
		if subscription.IAMAccessPolicy.IAMAccess == nil {
			ps.logger.Debug().Str("subscription", subscription.Name).Msg("No IAM access policies defined for subscription, skipping.")
			continue
		}
		for _, iamPolicy := range subscription.IAMAccessPolicy.IAMAccess {
			// Assuming iamPolicy.Name directly provides the service account ID/name
			// Corrected: use iamPolicy.Name directly as the service account.
			serviceAccount := iamPolicy.Name
			serviceAccountEmail, err := ps.iamClient.EnsureServiceAccountExists(ctx, projectID, serviceAccount)
			if err != nil {
				return fmt.Errorf("failed to ensure service account %s for Pub/Sub subscription %s: %w", serviceAccount, subscription.Name, err)
			}
			if err := ps.iamClient.AddPubSubTopicIAMBinding(ctx, subscription.Name, iamPolicy.Role, serviceAccountEmail); err != nil {
				return fmt.Errorf("failed to add IAM binding for Pub/Sub subscription %s: %w", subscription.Name, err)
			}
			ps.logger.Info().Str("subscription", subscription.Name).Str("role", iamPolicy.Role).Msg("Applied IAM binding for Pub/Sub subscription.")
		}
	}
	return nil
}

func (ps *IAMProvisioner) teardownSubscriptions(ctx context.Context, projectID string, subscriptions []servicemanager.SubscriptionConfig) {
	for _, subscription := range subscriptions { // Correctly using .Topics as per types.go
		if subscription.IAMAccessPolicy.IAMAccess == nil {
			ps.logger.Debug().Str("subscription", subscription.Name).Msg("No IAM access policies defined for subscription, skipping teardown.")
			continue
		}
		for _, iamPolicy := range subscription.IAMAccessPolicy.IAMAccess {
			// Assuming iamPolicy.Name directly provides the service account email or ID
			serviceAccount := iamPolicy.Name
			// Ensure we have the full email for removal, even if the service account might have been deleted already.
			// This call might fail if the SA is gone, but we proceed to attempt binding removal.
			serviceAccountEmail, err := ps.iamClient.EnsureServiceAccountExists(ctx, projectID, serviceAccount)
			if err != nil {
				ps.logger.Warn().Err(err).Str("service_account", serviceAccount).Msg("Could not resolve service account email for IAM teardown. Attempting removal with provided SA name.")
				serviceAccountEmail = fmt.Sprintf("%s@%s.iam.gserviceaccount.com", serviceAccount, projectID) // Fallback to assumed email format
			}

			ps.logger.Info().Str("subscription", subscription.Name).Str("role", iamPolicy.Role).Str("service_account_email", serviceAccountEmail).Msg("Attempting to remove IAM binding for Pub/Sub subscription.")
			// This assumes an `iamClient.RemovePubSubTopicIAMBinding` exists.
			// If not, this is a logical placeholder that needs implementation in the IAM client.
			if err := ps.iamClient.RemovePubSubTopicIAMBinding(ctx, subscription.Name, iamPolicy.Role, serviceAccountEmail); err != nil {
				ps.logger.Error().Err(err).Msg("Failed to remove IAM binding for Pub/Sub subscription. Continuing...")
			}
		}
	}
}
