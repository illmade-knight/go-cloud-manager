package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
)

// DataflowManager handles the setup, teardown, and verification of resources.
// It orchestrates operations across different cloud resource types within a specific environment.
type DataflowManager struct {
	messagingManager IMessagingManager // Changed from *MessagingManager
	storageManager   IStorageManager   // Changed from *StorageManager
	bigqueryManager  IBigQueryManager  // Changed from *BigQueryManager
	environment      Environment
	logger           zerolog.Logger
}

// NewDataflowManager creates a new DataflowManager by first creating
// all necessary clients and sub-managers.
func NewDataflowManager(
	ctx context.Context,
	environment Environment,
	schemaRegistry map[string]interface{},
	logger zerolog.Logger,
) (*DataflowManager, error) {

	dfLogger := logger.With().Str("component", "DataflowManager").Logger()

	msgClient, err := CreateGoogleMessagingClient(ctx, environment.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("dataflowmanager: failed to create Messaging client: %w", err)
	}
	gcsClient, err := CreateGoogleGCSClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("dataflowmanager: failed to create GCS client: %w", err)
	}
	bqClient, err := CreateGoogleBigQueryClient(ctx, environment.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("dataflowmanager: failed to create BigQuery client: %w", err)
	}

	msgManager, err := NewMessagingManager(msgClient, dfLogger, environment)
	if err != nil {
		return nil, fmt.Errorf("dataflowmanager: failed to create MessagingManager: %w", err)
	}
	storeManager, err := NewStorageManager(gcsClient, dfLogger, environment)
	if err != nil {
		return nil, fmt.Errorf("dataflowmanager: failed to create StorageManager: %w", err)
	}
	bqManager, err := NewBigQueryManager(bqClient, dfLogger, schemaRegistry, environment)
	if err != nil {
		return nil, fmt.Errorf("dataflowmanager: failed to create BigQueryManager: %w", err)
	}

	return &DataflowManager{
		messagingManager: msgManager,
		storageManager:   storeManager,
		bigqueryManager:  bqManager,
		environment:      environment,
		logger:           dfLogger,
	}, nil
}

// NewDataflowManagerFromManagers is a constructor for testing, allowing pre-built managers to be injected.
func NewDataflowManagerFromManagers(
	messagingManager IMessagingManager,
	storageManager IStorageManager,
	bigqueryManager IBigQueryManager,
	environment Environment,
	logger zerolog.Logger,
) (*DataflowManager, error) {
	if messagingManager == nil || storageManager == nil || bigqueryManager == nil {
		return nil, fmt.Errorf("all managers must be non-nil")
	}
	return &DataflowManager{
		messagingManager: messagingManager,
		storageManager:   storageManager,
		bigqueryManager:  bigqueryManager,
		environment:      environment,
		logger:           logger.With().Str("component", "DataflowManager").Logger(),
	}, nil
}

// CreateResources creates all configured resources for a dataflow concurrently.
func (dfm *DataflowManager) CreateResources(ctx context.Context, resources CloudResourcesSpec) (*ProvisionedResources, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	errChan := make(chan error, 3)
	newResources := &ProvisionedResources{}

	wg.Add(3)

	// Setup Messaging resources in parallel
	go func() {
		defer wg.Done()
		provTopics, provSubs, err := dfm.messagingManager.CreateResources(ctx, resources)
		if err != nil {
			errChan <- fmt.Errorf("messaging setup failed: %w", err)
		}
		mu.Lock()
		newResources.Topics = provTopics
		newResources.Subscriptions = provSubs
		mu.Unlock()
	}()

	// Setup Storage resources in parallel
	go func() {
		defer wg.Done()
		provBuckets, err := dfm.storageManager.CreateResources(ctx, resources)
		if err != nil {
			errChan <- fmt.Errorf("storage setup failed: %w", err)
		}
		mu.Lock()
		newResources.GCSBuckets = provBuckets
		mu.Unlock()
	}()

	// Setup BigQuery resources in parallel
	go func() {
		defer wg.Done()
		provTables, provDatasets, err := dfm.bigqueryManager.CreateResources(ctx, resources)
		if err != nil {
			errChan <- fmt.Errorf("bigquery setup failed: %w", err)
		}
		mu.Lock()
		newResources.BigQueryTables = provTables
		newResources.BigQueryDatasets = provDatasets
		mu.Unlock()
	}()

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		dfm.logger.Error().Int("error_count", len(allErrors)).Msg("Resource setup completed with errors.")
		// Return partial results along with the error
		return newResources, errors.Join(allErrors...)
	}

	dfm.logger.Info().Msg("All resource setup completed successfully.")
	return newResources, nil
}

// TeardownResources deletes resources for a specific dataflow sequentially.
func (dfm *DataflowManager) TeardownResources(ctx context.Context, resources CloudResourcesSpec) error {
	var allErrors []error

	// Teardown sequentially to respect dependencies, collecting errors along the way.
	if err := dfm.bigqueryManager.Teardown(ctx, resources); err != nil {
		allErrors = append(allErrors, fmt.Errorf("BigQuery teardown failed: %w", err))
		dfm.logger.Error().Err(err).Msg("Error during BigQuery teardown, continuing...")
	}
	if err := dfm.storageManager.Teardown(ctx, resources); err != nil {
		allErrors = append(allErrors, fmt.Errorf("GCS teardown failed: %w", err))
		dfm.logger.Error().Err(err).Msg("Error during GCS teardown, continuing...")
	}
	if err := dfm.messagingManager.Teardown(ctx, resources); err != nil {
		allErrors = append(allErrors, fmt.Errorf("Messaging teardown failed: %w", err))
		dfm.logger.Error().Err(err).Msg("Error during Messaging teardown, continuing...")
	}

	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}

	dfm.logger.Info().Msg("All resource teardown completed successfully.")
	return nil
}

// Verify checks all dataflow resources concurrently.
func (dfm *DataflowManager) Verify(ctx context.Context, resources CloudResourcesSpec) error {
	var wg sync.WaitGroup
	errChan := make(chan error, 3)

	wg.Add(3)

	// Verify Messaging resources in parallel
	go func() {
		defer wg.Done()
		if err := dfm.messagingManager.Verify(ctx, resources); err != nil {
			errChan <- fmt.Errorf("messaging verification failed: %w", err)
		}
	}()

	// Verify Storage resources in parallel
	go func() {
		defer wg.Done()
		if err := dfm.storageManager.Verify(ctx, resources); err != nil {
			errChan <- fmt.Errorf("storage verification failed: %w", err)
		}
	}()

	// Verify BigQuery resources in parallel
	go func() {
		defer wg.Done()
		if err := dfm.bigqueryManager.Verify(ctx, resources); err != nil {
			errChan <- fmt.Errorf("bigquery verification failed: %w", err)
		}
	}()

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		dfm.logger.Error().Int("error_count", len(allErrors)).Msg("Resource verification completed with errors.")
		return errors.Join(allErrors...)
	}

	dfm.logger.Info().Msg("All resources verified successfully.")
	return nil
}
