package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
)

// --- New Schema Registry System ---

// registeredSchemas holds a global mapping from a schema type name to an instance of the Go struct.
var registeredSchemas = make(map[string]interface{})
var registryMu sync.RWMutex

// RegisterSchema allows other packages to register their BigQuery schema structs.
// This is typically called from an init() function in the package where the struct is defined.
func RegisterSchema(name string, schemaStruct interface{}) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registeredSchemas[name]; exists {
		// In a real application, you might panic here or log a fatal error,
		// as this indicates a programming error (duplicate registration).
		panic(fmt.Sprintf("schema with name '%s' is already registered", name))
	}
	registeredSchemas[name] = schemaStruct
}

// verifySchemaRegistry is now fully implemented. It looks up types from the central registry.
func verifySchemaRegistry(tables []BigQueryTable) error {
	registryMu.RLock()
	defer registryMu.RUnlock()

	for _, table := range tables {
		if table.SchemaType != "" {
			_, ok := registeredSchemas[table.SchemaType]
			if !ok {
				return fmt.Errorf("unknown schema type '%s' for table '%s'. Is it registered?", table.SchemaType, table.Name)
			}
		}
	}
	return nil
}

// --- The Rest of the File (with updates) ---

// ResourceType defines a type-safe key for the managers map.
type ResourceType string

const (
	MessagingResourceType ResourceType = "messaging"
	StorageResourceType   ResourceType = "storage"
	BigQueryResourceType  ResourceType = "bigquery"
)

// ServiceManager coordinates all resource-specific operations by delegating to specialized managers.
type ServiceManager struct {
	managers map[ResourceType]IManager
	logger   zerolog.Logger
	writer   ProvisionedResourceWriter
}

// NewServiceManager creates a new central manager, inspecting the architecture to determine
// which sub-managers are needed.
func NewServiceManager(ctx context.Context, arch *MicroserviceArchitecture, writer ProvisionedResourceWriter, logger zerolog.Logger) (*ServiceManager, error) {
	sm := &ServiceManager{
		logger:   logger.With().Str("component", "ServiceManager").Logger(),
		writer:   writer,
		managers: make(map[ResourceType]IManager),
	}

	needsMessaging, needsStorage, needsBigQuery := scanArchitectureForResources(arch)

	if needsMessaging {
		sm.logger.Info().Msg("Messaging resources found, initializing MessagingManager.")
		msgClient, err := CreateGoogleMessagingClient(ctx, arch.ProjectID)
		if err != nil {
			return nil, err
		}
		sm.managers[MessagingResourceType], err = NewMessagingManager(msgClient, logger, arch.Environment)
		if err != nil {
			return nil, err
		}
	}

	if needsStorage {
		sm.logger.Info().Msg("Storage resources found, initializing StorageManager.")
		gcsClient, err := CreateGoogleGCSClient(ctx)
		if err != nil {
			return nil, err
		}
		sm.managers[StorageResourceType], err = NewStorageManager(gcsClient, logger, arch.Environment)
		if err != nil {
			return nil, err
		}
	}

	if needsBigQuery {
		sm.logger.Info().Msg("BigQuery resources found, initializing BigQueryManager.")
		bqClient, err := CreateGoogleBigQueryClient(ctx, arch.ProjectID)
		if err != nil {
			return nil, err
		}
		sm.managers[BigQueryResourceType], err = NewBigQueryManager(bqClient, logger, arch.Environment)
		if err != nil {
			return nil, err
		}
	}

	return sm, nil
}

// NewServiceManagerFromClients remains for testing purposes.
func NewServiceManagerFromClients(mc MessagingClient, sc StorageClient, bc BQClient, environment Environment, schemaRegistry map[string]interface{}, writer ProvisionedResourceWriter, logger zerolog.Logger) (*ServiceManager, error) {
	sm := &ServiceManager{
		logger:   logger.With().Str("component", "ServiceManager").Logger(),
		writer:   writer,
		managers: make(map[ResourceType]IManager),
	}

	var err error
	if mc != nil {
		sm.managers[MessagingResourceType], err = NewMessagingManager(mc, logger, environment)
		if err != nil {
			return nil, fmt.Errorf("failed to create Messaging manager from client: %w", err)
		}
	}
	if sc != nil {
		sm.managers[StorageResourceType], err = NewStorageManager(sc, logger, environment)
		if err != nil {
			return nil, fmt.Errorf("failed to create Storage manager from client: %w", err)
		}
	}
	if bc != nil {
		sm.managers[BigQueryResourceType], err = NewBigQueryManager(bc, logger, environment)
		if err != nil {
			return nil, fmt.Errorf("failed to create BigQuery manager from client: %w", err)
		}
	}

	return sm, nil
}

// NewServiceManagerFromManagers remains for testing purposes.
func NewServiceManagerFromManagers(mm IMessagingManager, sm IStorageManager, bqm IBigQueryManager, writer ProvisionedResourceWriter, logger zerolog.Logger) (*ServiceManager, error) {
	svcMgr := &ServiceManager{
		logger:   logger.With().Str("component", "ServiceManager").Logger(),
		writer:   writer,
		managers: make(map[ResourceType]IManager),
	}
	if mm != nil {
		svcMgr.managers[MessagingResourceType] = mm
	}
	if sm != nil {
		svcMgr.managers[StorageResourceType] = sm
	}
	if bqm != nil {
		svcMgr.managers[BigQueryResourceType] = bqm
	}
	return svcMgr, nil
}

// SetupDataflow now dynamically builds the schema registry map before creating resources.
func (sm *ServiceManager) SetupDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) (*ProvisionedResources, error) {
	sm.logger.Info().Str("dataflow", dataflowName).Msg("Starting setup for specific dataflow")
	targetDataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return nil, fmt.Errorf("dataflow spec '%s' not found in architecture", dataflowName)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	errChan := make(chan error, len(sm.managers))
	provisioned := &ProvisionedResources{}

	if mgr, ok := sm.managers[MessagingResourceType]; ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			provTopics, provSubs, err := mgr.(IMessagingManager).CreateResources(ctx, targetDataflow.Resources)
			if err != nil {
				errChan <- err
				return
			}
			mu.Lock()
			provisioned.Topics = provTopics
			provisioned.Subscriptions = provSubs
			mu.Unlock()
		}()
	}

	if mgr, ok := sm.managers[StorageResourceType]; ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			provBuckets, err := mgr.(IStorageManager).CreateResources(ctx, targetDataflow.Resources)
			if err != nil {
				errChan <- err
				return
			}
			mu.Lock()
			provisioned.GCSBuckets = provBuckets
			mu.Unlock()
		}()
	}

	if mgr, ok := sm.managers[BigQueryResourceType]; ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := verifySchemaRegistry(targetDataflow.Resources.BigQueryTables)
			if err != nil {
				errChan <- err
				return
			}

			provTables, provDatasets, err := mgr.(IBigQueryManager).CreateResources(ctx, targetDataflow.Resources)
			if err != nil {
				errChan <- err
				return
			}
			mu.Lock()
			provisioned.BigQueryTables = provTables
			provisioned.BigQueryDatasets = provDatasets
			mu.Unlock()
		}()
	}

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}
	return provisioned, errors.Join(allErrors...)
}

// TeardownDataflow is now much cleaner, with no type assertions needed.
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
	for _, manager := range sm.managers {
		if err := manager.Teardown(ctx, targetDataflow.Resources); err != nil {
			allErrors = append(allErrors, err)
		}
	}
	return errors.Join(allErrors...)
}

// VerifyDataflow is also cleaner, with no type assertions needed.
func (sm *ServiceManager) VerifyDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) error {
	sm.logger.Info().Str("dataflow", dataflowName).Msg("Starting verification for specific dataflow")
	targetDataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow spec '%s' not found in architecture", dataflowName)
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(sm.managers))

	for _, manager := range sm.managers {
		wg.Add(1)
		go func(mgr IManager) {
			defer wg.Done()
			errChan <- mgr.Verify(ctx, targetDataflow.Resources)
		}(manager)
	}

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

// GetMessagingManager provides access to the messaging sub-manager.
func (sm *ServiceManager) GetMessagingManager() IMessagingManager {
	if mgr, ok := sm.managers[MessagingResourceType]; ok {
		return mgr.(IMessagingManager)
	}
	return nil
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

// --- Private Helper Functions ---

func scanArchitectureForResources(arch *MicroserviceArchitecture) (needsMessaging bool, needsStorage bool, needsBigQuery bool) {
	for _, df := range arch.Dataflows {
		if len(df.Resources.Topics) > 0 || len(df.Resources.Subscriptions) > 0 {
			needsMessaging = true
		}
		if len(df.Resources.GCSBuckets) > 0 {
			needsStorage = true
		}
		if len(df.Resources.BigQueryDatasets) > 0 || len(df.Resources.BigQueryTables) > 0 {
			needsBigQuery = true
		}
	}
	return
}
