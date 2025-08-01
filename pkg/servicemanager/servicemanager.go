/*
Package servicemanager provides tools to declaratively manage cloud infrastructure resources
based on a YAML configuration. It is designed to be extensible, allowing for different
cloud providers and resource types to be plugged in.

The core workflow is:
 1. Define an entire microservice architecture, including services and the resources they
    depend on (e.g., Pub/Sub topics, GCS buckets), in one or more YAML files.
 2. Load this configuration into the `MicroserviceArchitecture` struct.
 3. Create a `ServiceManager`, which acts as a high-level orchestrator.
 4. Use the `ServiceManager` to provision, verify, or tear down the resources defined
    in the architecture for specific dataflows or for the entire system at once.

The manager achieves this by delegating tasks to specialized sub-managers (e.g.,
`MessagingManager`, `StorageManager`), which are backed by provider-specific adapters
(e.g., for Google Cloud Platform).
*/
package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
)

// --- Schema Registry System ---

// registeredSchemas holds a global mapping from a schema type name (e.g., "MyEventSchema")
// to an instance of the corresponding Go struct. This allows the BigQueryManager to infer
// table schemas without being tightly coupled to the packages where those structs are defined.
// Access is controlled by a RWMutex to ensure thread safety.
var registeredSchemas = make(map[string]interface{})
var registryMu sync.RWMutex

// RegisterSchema allows other packages to register their BigQuery schema structs.
// This function is designed to be called from an init() function in the package
// where the schema struct is defined. This ensures that by the time the ServiceManager
// is initialized, all necessary schemas are available in the registry.
//
// Example Usage (in another package):
//
//	type MyEvent struct {
//	    ID string `bigquery:"id"`
//	    Timestamp time.Time `bigquery:"timestamp"`
//	}
//
//	func init() {
//	    servicemanager.RegisterSchema("MyEventSchema", MyEvent{})
//	}
func RegisterSchema(name string, schemaStruct interface{}) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registeredSchemas[name]; exists {
		// This indicates a programming error (two packages trying to register the same schema name).
		// In a real application, panicking is appropriate to fail fast during startup.
		panic(fmt.Sprintf("schema with name '%s' is already registered", name))
	}
	registeredSchemas[name] = schemaStruct
}

// verifySchemaRegistry checks if all schema types required by a set of tables
// exist in the central registry.
func verifySchemaRegistry(tables []BigQueryTable) error {
	registryMu.RLock()
	defer registryMu.RUnlock()

	for _, table := range tables {
		if table.SchemaType != "" {
			_, ok := registeredSchemas[table.SchemaType]
			if !ok {
				return fmt.Errorf("unknown schema type '%s' for table '%s'. Is it registered via an init() function?", table.SchemaType, table.Name)
			}
		}
	}
	return nil
}

// ResourceType defines a type-safe key for the managers map.
type ResourceType string

const (
	MessagingResourceType ResourceType = "messaging"
	StorageResourceType   ResourceType = "storage"
	BigQueryResourceType  ResourceType = "bigquery"
)

// ServiceManager coordinates all resource-specific operations by delegating to specialized managers.
// It is the primary entry point for orchestrating infrastructure setup and teardown.
type ServiceManager struct {
	managers map[ResourceType]IManager
	logger   zerolog.Logger
	writer   ProvisionedResourceWriter
}

// NewServiceManager creates a new central manager. It scans the provided architecture
// to determine which resource types are needed and initializes the corresponding
// sub-managers (e.g., MessagingManager, StorageManager) automatically.
func NewServiceManager(ctx context.Context, arch *MicroserviceArchitecture, writer ProvisionedResourceWriter, logger zerolog.Logger) (*ServiceManager, error) {
	sm := &ServiceManager{
		logger:   logger.With().Str("component", "ServiceManager").Logger(),
		writer:   writer,
		managers: make(map[ResourceType]IManager),
	}

	needsMessaging, needsStorage, needsBigQuery := scanArchitectureForResources(arch)
	var err error

	if needsMessaging {
		sm.logger.Info().Msg("Messaging resources found, initializing MessagingManager.")
		var msgClient MessagingClient
		msgClient, err = CreateGoogleMessagingClient(ctx, arch.ProjectID)
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
		var gcsClient StorageClient
		gcsClient, err = CreateGoogleGCSClient(ctx)
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
		var bqClient BQClient
		bqClient, err = CreateGoogleBigQueryClient(ctx, arch.ProjectID)
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

// NewServiceManagerFromClients creates a ServiceManager with pre-configured clients.
// This constructor is primarily used for testing with mocks or emulators.
func NewServiceManagerFromClients(mc MessagingClient, sc StorageClient, bc BQClient, environment Environment, writer ProvisionedResourceWriter, logger zerolog.Logger) (*ServiceManager, error) {
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

// NewServiceManagerFromManagers creates a ServiceManager with pre-configured manager instances.
// This constructor is primarily used for unit testing the ServiceManager's orchestration logic.
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

// SetupDataflow provisions all resources defined within a specific dataflow (ResourceGroup).
// It runs the creation process for each resource type (messaging, storage, etc.) concurrently.
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

	// For each resource type, if its manager exists, start a concurrent creation process.
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
			// BQ resources have a dependency on the schema registry, which must be checked first.
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
			// A mutex is required to safely write to the shared 'provisioned' struct from multiple goroutines.
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

// TeardownDataflow deletes all resources within a specific dataflow.
// It will only act if the dataflow's lifecycle strategy is 'ephemeral'.
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
		err := manager.Teardown(ctx, targetDataflow.Resources)
		if err != nil {
			allErrors = append(allErrors, err)
		}
	}
	return errors.Join(allErrors...)
}

// VerifyDataflow checks for the existence of all resources within a specific dataflow.
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

// SetupAll runs the setup process for all dataflows defined in the architecture.
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

// TeardownAll runs the teardown process for all ephemeral dataflows.
func (sm *ServiceManager) TeardownAll(ctx context.Context, arch *MicroserviceArchitecture) error {
	sm.logger.Info().Str("environment", arch.Environment.Name).Msg("Starting full environment teardown for all ephemeral dataflows...")
	var allErrors []error

	for dfName := range arch.Dataflows {
		err := sm.TeardownDataflow(ctx, arch, dfName)
		if err != nil {
			allErrors = append(allErrors, fmt.Errorf("failed to teardown dataflow '%s': %w", dfName, err))
		}
	}

	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}

	sm.logger.Info().Msg("Full environment teardown completed.")
	return nil
}

// scanArchitectureForResources is a helper that inspects the entire architecture to see
// which types of resources are present, so that only the necessary sub-managers are initialized.
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
