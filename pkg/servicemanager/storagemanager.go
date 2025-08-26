package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
)

// ErrBucketNotExist is a standard error returned when a storage bucket is not found.
// This allows the manager to check for this condition without depending on a specific
// cloud provider's error types.
var ErrBucketNotExist = errors.New("storage: bucket does not exist")

// StorageManager handles the creation, update, verification, and deletion of storage buckets.
// It orchestrates these operations through a generic StorageClient interface.
type StorageManager struct {
	client      StorageClient
	logger      zerolog.Logger
	environment Environment
}

// NewStorageManager creates a new manager for orchestrating storage bucket resources.
func NewStorageManager(client StorageClient, logger zerolog.Logger, environment Environment) (*StorageManager, error) {
	if client == nil {
		return nil, errors.New("storage client (StorageClient interface) cannot be nil")
	}
	return &StorageManager{
		client:      client,
		logger:      logger.With().Str("component", "StorageManager").Logger(),
		environment: environment,
	}, nil
}

// CreateResources creates new buckets or updates existing ones based on the provided spec.
// This operation is idempotent and can be run multiple times safely.
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
			log := sm.logger.With().Str("bucket", cfg.Name).Logger()
			bucketHandle := sm.client.Bucket(cfg.Name)
			_, err := bucketHandle.Attrs(ctx)

			// If err is nil, the bucket exists, so we update it.
			if err == nil {
				log.Info().Msg("Bucket already exists, attempting to update.")
				updateAttrs := BucketAttributesToUpdate{
					StorageClass:      &cfg.StorageClass,
					VersioningEnabled: cfg.VersioningEnabled,
					Labels:            cfg.Labels,
					LifecycleRules:    &cfg.LifecycleRules,
				}
				_, updateErr := bucketHandle.Update(ctx, updateAttrs)
				if updateErr != nil {
					errChan <- fmt.Errorf("failed to update bucket '%s': %w", cfg.Name, updateErr)
				} else {
					log.Info().Msg("Bucket updated successfully.")
					provisionedChan <- ProvisionedGCSBucket{Name: cfg.Name}
				}
				return
			}

			// If the error is anything other than our generic "not found" error, it's an unexpected issue.
			if !errors.Is(err, ErrBucketNotExist) {
				errChan <- fmt.Errorf("failed to check existence of bucket '%s': %w", cfg.Name, err)
				return
			}

			// Bucket does not exist, so we create it.
			log.Info().Msg("Bucket does not exist, creating.")
			createAttrs := &BucketAttributes{
				Name:              cfg.Name,
				Location:          cfg.Location,
				StorageClass:      cfg.StorageClass,
				VersioningEnabled: cfg.VersioningEnabled,
				Labels:            cfg.Labels,
				LifecycleRules:    cfg.LifecycleRules,
			}
			createErr := bucketHandle.Create(ctx, sm.environment.ProjectID, createAttrs)
			if createErr != nil {
				errChan <- fmt.Errorf("failed to create bucket '%s': %w", cfg.Name, createErr)
			} else {
				log.Info().Msg("Bucket created successfully.")
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

// Teardown deletes all specified storage buckets concurrently.
// It will skip buckets that have teardown protection enabled.
func (sm *StorageManager) Teardown(ctx context.Context, resources CloudResourcesSpec) error {
	sm.logger.Info().Msg("Starting Storage Bucket teardown")

	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.GCSBuckets))

	for _, bucketCfg := range resources.GCSBuckets {
		wg.Add(1)
		go func(cfg GCSBucket) {
			defer wg.Done()
			log := sm.logger.With().Str("bucket", cfg.Name).Logger()

			if cfg.TeardownProtection {
				log.Warn().Msg("Teardown protection enabled, skipping deletion.")
				return
			}
			if cfg.Name == "" {
				sm.logger.Warn().Msg("Skipping bucket with empty name during teardown")
				return
			}

			bucketHandle := sm.client.Bucket(cfg.Name)
			_, err := bucketHandle.Attrs(ctx)
			if errors.Is(err, ErrBucketNotExist) {
				log.Info().Msg("Bucket does not exist, skipping deletion.")
				return
			}

			log.Info().Msg("Attempting to delete bucket...")
			deleteErr := bucketHandle.Delete(ctx)
			if deleteErr != nil {
				// Provide a more specific error for the common "bucket not empty" case.
				if strings.Contains(strings.ToLower(deleteErr.Error()), "not empty") {
					errChan <- fmt.Errorf("failed to delete bucket '%s' because it is not empty", cfg.Name)
				} else {
					errChan <- fmt.Errorf("failed to delete bucket '%s': %w", cfg.Name, deleteErr)
				}
			} else {
				log.Info().Msg("Bucket deleted successfully.")
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
		return fmt.Errorf("storage Bucket teardown completed with errors: %w", errors.Join(allErrors...))
	}

	sm.logger.Info().Msg("storage Bucket teardown completed successfully.")
	return nil
}

// Verify checks if all specified GCS buckets exist concurrently.
func (sm *StorageManager) Verify(ctx context.Context, resources CloudResourcesSpec) error {
	sm.logger.Info().Msg("Verifying Storage Buckets...")

	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.GCSBuckets))

	for _, bucketCfg := range resources.GCSBuckets {
		wg.Add(1)
		go func(cfg GCSBucket) {
			defer wg.Done()
			if cfg.Name == "" {
				sm.logger.Warn().Msg("Skipping verification for bucket with empty name")
				return
			}

			bucketHandle := sm.client.Bucket(cfg.Name)
			_, err := bucketHandle.Attrs(ctx)

			if errors.Is(err, ErrBucketNotExist) {
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
		return fmt.Errorf("storage Bucket verification completed with errors: %w", errors.Join(allErrors...))
	}

	sm.logger.Info().Msg("Storage Bucket verification completed successfully.")
	return nil
}
