package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"google.golang.org/api/googleapi"
)

// StorageManager handles the creation and deletion of storage buckets.
type StorageManager struct {
	client      StorageClient
	logger      zerolog.Logger
	environment Environment
}

// NewStorageManager creates a new StorageManager.
func NewStorageManager(client StorageClient, logger zerolog.Logger, environment Environment) (*StorageManager, error) {
	if client == nil {
		return nil, fmt.Errorf("storage client (StorageClient interface) cannot be nil")
	}
	return &StorageManager{
		client:      client,
		logger:      logger.With().Str("component", "StorageManager").Logger(),
		environment: environment,
	}, nil
}

// isBucketNotExist checks if an error signifies that a bucket does not exist.
func isBucketNotExist(err error) bool {
	if errors.Is(err, Done) {
		return true
	}
	var gapiErr *googleapi.Error
	if errors.As(err, &gapiErr) && gapiErr.Code == 404 {
		return true
	}
	if err != nil && strings.Contains(err.Error(), "storage: bucket doesn't exist") {
		return true
	}
	return false
}

// CreateResources creates or updates storage buckets concurrently.
func (sm *StorageManager) CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedGCSBucket, error) {
	sm.logger.Info().Str("project_id", sm.environment.ProjectID).Msg("Starting Storage Bucket setup")

	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.GCSBuckets))
	provisionedChan := make(chan ProvisionedGCSBucket, len(resources.GCSBuckets))

	for _, bucketCfg := range resources.GCSBuckets {
		if bucketCfg.Name == "" {
			sm.logger.Warn().Msg("Skipping bucket with empty name during setup")
			continue
		}

		wg.Add(1)
		go func(cfg GCSBucket) {
			defer wg.Done()
			bucketHandle := sm.client.Bucket(cfg.Name)
			_, err := bucketHandle.Attrs(ctx)

			if err == nil {
				// Bucket exists, so update it.
				sm.logger.Info().Str("bucket_name", cfg.Name).Msg("Bucket already exists, attempting to update.")
				updateAttrs := BucketAttributesToUpdate{
					StorageClass:      &cfg.StorageClass,
					VersioningEnabled: cfg.VersioningEnabled,
					Labels:            cfg.Labels,
					LifecycleRules:    &cfg.LifecycleRules,
				}
				if _, err := bucketHandle.Update(ctx, updateAttrs); err != nil {
					errChan <- fmt.Errorf("failed to update bucket '%s': %w", cfg.Name, err)
				} else {
					sm.logger.Info().Str("bucket_name", cfg.Name).Msg("Bucket updated successfully.")
					provisionedChan <- ProvisionedGCSBucket{Name: cfg.Name}
				}
				return
			}

			if !isBucketNotExist(err) {
				errChan <- fmt.Errorf("failed to check existence of bucket '%s': %w", cfg.Name, err)
				return
			}

			// Bucket does not exist, so create it.
			sm.logger.Info().Str("bucket_name", cfg.Name).Msg("Bucket does not exist, creating.")
			createAttrs := &BucketAttributes{
				Name:              cfg.Name,
				Location:          cfg.Location,
				StorageClass:      cfg.StorageClass,
				VersioningEnabled: cfg.VersioningEnabled,
				Labels:            cfg.Labels,
				LifecycleRules:    cfg.LifecycleRules,
			}
			if err := bucketHandle.Create(ctx, sm.environment.ProjectID, createAttrs); err != nil {
				errChan <- fmt.Errorf("failed to create bucket '%s': %w", cfg.Name, err)
			} else {
				sm.logger.Info().Str("bucket_name", cfg.Name).Msg("Bucket created successfully.")
				provisionedChan <- ProvisionedGCSBucket{Name: cfg.Name}
			}
		}(bucketCfg)
	}

	wg.Wait()
	close(errChan)
	close(provisionedChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	var provisionedBuckets []ProvisionedGCSBucket
	for bucket := range provisionedChan {
		provisionedBuckets = append(provisionedBuckets, bucket)
	}

	if len(allErrors) > 0 {
		return provisionedBuckets, errors.Join(allErrors...)
	}

	sm.logger.Info().Msg("Storage Bucket setup completed successfully.")
	return provisionedBuckets, nil
}

// Teardown deletes storage buckets concurrently.
func (sm *StorageManager) Teardown(ctx context.Context, resources CloudResourcesSpec) error {
	sm.logger.Info().Msg("Starting Storage Bucket teardown")

	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.GCSBuckets))

	for _, bucketCfg := range resources.GCSBuckets {
		if bucketCfg.TeardownProtection {
			sm.logger.Warn().Str("name", bucketCfg.Name).Msg("teardown protection in place, skipping")
			continue
		}
		if bucketCfg.Name == "" {
			sm.logger.Warn().Msg("Skipping bucket with empty name during teardown")
			continue
		}

		wg.Add(1)
		go func(cfg GCSBucket) {
			defer wg.Done()
			bucketHandle := sm.client.Bucket(cfg.Name)

			if _, err := bucketHandle.Attrs(ctx); isBucketNotExist(err) {
				sm.logger.Info().Str("bucket_name", cfg.Name).Msg("Bucket does not exist, skipping deletion.")
				return
			}

			sm.logger.Info().Str("bucket_name", cfg.Name).Msg("Attempting to delete bucket...")
			if err := bucketHandle.Delete(ctx); err != nil {
				// Provide a more specific error for the common "bucket not empty" case.
				if strings.Contains(strings.ToLower(err.Error()), "not empty") {
					errChan <- fmt.Errorf("failed to delete bucket '%s' because it is not empty", cfg.Name)
				} else {
					errChan <- fmt.Errorf("failed to delete bucket '%s': %w", cfg.Name, err)
				}
			} else {
				sm.logger.Info().Str("bucket_name", cfg.Name).Msg("Bucket deleted successfully.")
			}
		}(bucketCfg)
	}

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("Storage Bucket teardown completed with errors: %w", errors.Join(allErrors...))
	}

	sm.logger.Info().Msg("Storage Bucket teardown completed successfully.")
	return nil
}

// Verify checks if the specified GCS buckets exist concurrently.
func (sm *StorageManager) Verify(ctx context.Context, resources CloudResourcesSpec) error {
	sm.logger.Info().Msg("Verifying Storage Buckets...")

	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.GCSBuckets))

	for _, bucketCfg := range resources.GCSBuckets {
		if bucketCfg.Name == "" {
			sm.logger.Warn().Msg("Skipping verification for bucket with empty name")
			continue
		}

		wg.Add(1)
		go func(cfg GCSBucket) {
			defer wg.Done()
			bucketHandle := sm.client.Bucket(cfg.Name)
			_, err := bucketHandle.Attrs(ctx)

			if isBucketNotExist(err) {
				errChan <- fmt.Errorf("bucket '%s' not found", cfg.Name)
			} else if err != nil {
				errChan <- fmt.Errorf("failed to verify bucket '%s': %w", cfg.Name, err)
			}
		}(bucketCfg)
	}

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("Storage Bucket verification completed with errors: %w", errors.Join(allErrors...))
	}

	sm.logger.Info().Msg("Storage Bucket verification completed successfully.")
	return nil
}
