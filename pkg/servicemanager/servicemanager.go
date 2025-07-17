package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
)

// ServiceManager coordinates all resource-specific operations by delegating to specialized managers.
type ServiceManager struct {
	messagingManager IMessagingManager
	storageManager   IStorageManager
	bigqueryManager  IBigQueryManager
	logger           zerolog.Logger
	writer           ProvisionedResourceWriter
}

// NewServiceManager creates a new central manager, initializing all required clients for a production environment.
func NewServiceManager(ctx context.Context, environment Environment, schemaRegistry map[string]interface{}, writer ProvisionedResourceWriter, logger zerolog.Logger) (*ServiceManager, error) {
	msgClient, err := CreateGoogleMessagingClient(ctx, environment.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create Messaging client: %w", err)
	}
	gcsClient, err := CreateGoogleGCSClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	bqClient, err := CreateGoogleBigQueryClient(ctx, environment.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create BigQuery client: %w", err)
	}
	return NewServiceManagerFromClients(msgClient, gcsClient, bqClient, environment, schemaRegistry, writer, logger)
}

// NewServiceManagerFromClients creates a new central manager from pre-existing clients, perfect for testing.
func NewServiceManagerFromClients(mc MessagingClient, sc StorageClient, bc BQClient, environment Environment, schemaRegistry map[string]interface{}, writer ProvisionedResourceWriter, logger zerolog.Logger) (*ServiceManager, error) {
	sm := &ServiceManager{
		logger: logger.With().Str("component", "ServiceManager").Logger(),
		writer: writer,
	}

	if mc != nil {
		messagingManager, err := NewMessagingManager(mc, logger, environment)
		if err != nil {
			return nil, fmt.Errorf("failed to create Messaging manager: %w", err)
		}
		sm.messagingManager = messagingManager
	}

	if sc != nil {
		storageManager, err := NewStorageManager(sc, logger, environment)
		if err != nil {
			return nil, fmt.Errorf("failed to create Storage manager: %w", err)
		}
		sm.storageManager = storageManager
	}

	if bc != nil {
		bigqueryManager, err := NewBigQueryManager(bc, logger, schemaRegistry, environment)
		if err != nil {
			return nil, fmt.Errorf("failed to create BigQuery manager: %w", err)
		}
		sm.bigqueryManager = bigqueryManager
	}

	return sm, nil
}

func NewServiceManagerFromManagers(mm IMessagingManager, sm IStorageManager, bqm IBigQueryManager, writer ProvisionedResourceWriter, logger zerolog.Logger) (*ServiceManager, error) {
	smr := &ServiceManager{
		logger:           logger.With().Str("component", "ServiceManager").Logger(),
		writer:           writer,
		storageManager:   sm,
		messagingManager: mm,
		bigqueryManager:  bqm,
	}

	return smr, nil
}

// SetupDataflow creates resources for a *specific* dataflow with improved safety checks.
func (sm *ServiceManager) SetupDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) (*ProvisionedResources, error) {
	sm.logger.Info().Str("dataflow", dataflowName).Msg("Starting setup for specific dataflow")

	targetDataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return nil, fmt.Errorf("dataflow spec '%s' not found in architecture", dataflowName)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	errChan := make(chan error, 3)
	provisioned := &ProvisionedResources{}

	// Define operations for each resource type.
	operations := []struct {
		managerName  string
		manager      interface{}
		hasResources bool
		run          func()
	}{
		{
			managerName:  "Messaging",
			manager:      sm.messagingManager,
			hasResources: len(targetDataflow.Resources.Topics) > 0 || len(targetDataflow.Resources.Subscriptions) > 0,
			run: func() {
				defer wg.Done()
				provTopics, provSubs, err := sm.messagingManager.CreateResources(ctx, targetDataflow.Resources)
				if err != nil {
					errChan <- err
					return
				}
				mu.Lock()
				provisioned.Topics = provTopics
				provisioned.Subscriptions = provSubs
				mu.Unlock()
			},
		},
		{
			managerName:  "Storage",
			manager:      sm.storageManager,
			hasResources: len(targetDataflow.Resources.GCSBuckets) > 0,
			run: func() {
				defer wg.Done()
				provBuckets, err := sm.storageManager.CreateResources(ctx, targetDataflow.Resources)
				if err != nil {
					errChan <- err
					return
				}
				mu.Lock()
				provisioned.GCSBuckets = provBuckets
				mu.Unlock()
			},
		},
		{
			managerName:  "BigQuery",
			manager:      sm.bigqueryManager,
			hasResources: len(targetDataflow.Resources.BigQueryDatasets) > 0 || len(targetDataflow.Resources.BigQueryTables) > 0,
			run: func() {
				defer wg.Done()
				provTables, provDatasets, err := sm.bigqueryManager.CreateResources(ctx, targetDataflow.Resources)
				if err != nil {
					errChan <- err
					return
				}
				mu.Lock()
				provisioned.BigQueryTables = provTables
				provisioned.BigQueryDatasets = provDatasets
				mu.Unlock()
			},
		},
	}

	// Loop through operations, add to WaitGroup, and run if the manager exists.
	for _, op := range operations {
		wg.Add(1)
		if op.manager != nil {
			go op.run()
		} else {
			// If the manager is nil but resources were requested, log a warning.
			if op.hasResources {
				sm.logger.Warn().Str("dataflow", dataflowName).Msgf("Warning: Dataflow requests %s resources but the %s manager is not configured.", op.managerName, op.managerName)
			}
			wg.Done() // Decrement counter immediately if manager is nil.
		}
	}

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	return provisioned, errors.Join(allErrors...)
}

// TeardownDataflow tears down resources for a *specific* dataflow, now more robustly.
func (sm *ServiceManager) TeardownDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) error {
	sm.logger.Info().Str("dataflow", dataflowName).Msg("Starting teardown for specific dataflow")

	targetDataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow spec '%s' not found in architecture", dataflowName)
	}

	if targetDataflow.Lifecycle == nil || targetDataflow.Lifecycle.Strategy != LifecycleStrategyEphemeral {
		sm.logger.Warn().Str("dataflow", dataflowName).Msg("Teardown skipped: Dataflow not marked with 'ephemeral' lifecycle.")
		return nil
	}

	var allErrors []error
	if sm.bigqueryManager != nil {
		if err := sm.bigqueryManager.Teardown(ctx, targetDataflow.Resources); err != nil {
			allErrors = append(allErrors, err)
		}
	}
	if sm.storageManager != nil {
		if err := sm.storageManager.Teardown(ctx, targetDataflow.Resources); err != nil {
			allErrors = append(allErrors, err)
		}
	}
	if sm.messagingManager != nil {
		if err := sm.messagingManager.Teardown(ctx, targetDataflow.Resources); err != nil {
			allErrors = append(allErrors, err)
		}
	}

	return errors.Join(allErrors...)
}

// --- Passthrough and Helper Methods ---

// GetMessagingManager provides access to the messaging sub-manager.
func (sm *ServiceManager) GetMessagingManager() IMessagingManager {
	return sm.messagingManager
}

// SynthesizeAllResources creates a master view of all cloud resources required by all dataflows.
func (sm *ServiceManager) SynthesizeAllResources(arch *MicroserviceArchitecture) CloudResourcesSpec {
	allResources := CloudResourcesSpec{}
	for _, dataflow := range arch.Dataflows {
		allResources.Topics = append(allResources.Topics, dataflow.Resources.Topics...)
		allResources.Subscriptions = append(allResources.Subscriptions, dataflow.Resources.Subscriptions...)
		allResources.GCSBuckets = append(allResources.GCSBuckets, dataflow.Resources.GCSBuckets...)
		allResources.BigQueryDatasets = append(allResources.BigQueryDatasets, dataflow.Resources.BigQueryDatasets...)
		allResources.BigQueryTables = append(allResources.BigQueryTables, dataflow.Resources.BigQueryTables...)
	}
	return allResources
}

// SetupAll runs the setup process for all dataflows concurrently.
func (sm *ServiceManager) SetupAll(ctx context.Context, arch *MicroserviceArchitecture) (*ProvisionedResources, error) {
	sm.logger.Info().Str("environment", arch.Environment.Name).Msg("Starting full environment setup for all dataflows...")

	var wg sync.WaitGroup
	var mu sync.Mutex
	errChan := make(chan error, len(arch.Dataflows))
	allProvResources := &ProvisionedResources{}

	for dfName := range arch.Dataflows {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			provRes, err := sm.SetupDataflow(ctx, arch, name)
			if err != nil {
				errChan <- fmt.Errorf("failed to setup dataflow '%s': %w", name, err)
				return
			}
			mu.Lock()
			allProvResources.Topics = append(allProvResources.Topics, provRes.Topics...)
			allProvResources.Subscriptions = append(allProvResources.Subscriptions, provRes.Subscriptions...)
			allProvResources.GCSBuckets = append(allProvResources.GCSBuckets, provRes.GCSBuckets...)
			allProvResources.BigQueryDatasets = append(allProvResources.BigQueryDatasets, provRes.BigQueryDatasets...)
			allProvResources.BigQueryTables = append(allProvResources.BigQueryTables, provRes.BigQueryTables...)
			mu.Unlock()
		}(dfName)
	}

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return allProvResources, errors.Join(allErrors...)
	}

	sm.logger.Info().Str("environment", arch.Environment.Name).Msg("Full environment setup completed successfully.")
	return allProvResources, nil
}

// TeardownAll runs the teardown process for all dataflows sequentially.
func (sm *ServiceManager) TeardownAll(ctx context.Context, arch *MicroserviceArchitecture) error {
	sm.logger.Info().Str("environment", arch.Environment.Name).Msg("Starting full environment teardown for all dataflows...")
	var allErrors []error

	for dfName := range arch.Dataflows {
		if err := sm.TeardownDataflow(ctx, arch, dfName); err != nil {
			allErrors = append(allErrors, fmt.Errorf("failed to teardown dataflow '%s': %w", dfName, err))
		}
	}

	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}

	sm.logger.Info().Msg("Full environment teardown completed.")
	return nil
}

// VerifyDataflow checks if all resources for a specific dataflow exist.
func (sm *ServiceManager) VerifyDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) error {
	sm.logger.Info().Str("dataflow", dataflowName).Msg("Starting verification for specific dataflow")

	targetDataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow spec '%s' not found in architecture", dataflowName)
	}

	var wg sync.WaitGroup
	errChan := make(chan error, 3)
	wg.Add(3)

	go func() {
		defer wg.Done()
		if sm.messagingManager != nil {
			errChan <- sm.messagingManager.Verify(ctx, targetDataflow.Resources)
		}
	}()
	go func() {
		defer wg.Done()
		if sm.storageManager != nil {
			errChan <- sm.storageManager.Verify(ctx, targetDataflow.Resources)
		}
	}()
	go func() {
		defer wg.Done()
		if sm.bigqueryManager != nil {
			errChan <- sm.bigqueryManager.Verify(ctx, targetDataflow.Resources)
		}
	}()

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		if err != nil {
			allErrors = append(allErrors, err)
		}
	}

	return errors.Join(allErrors...)
}
