package servicemanager

import (
	"cloud.google.com/go/bigquery"
	"context"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"net/http"
	"sync"
)

// --- BigQuery Client Abstraction Interfaces ---

// BQTable is our abstraction of a BigQuery table for ServiceManager operations.
type BQTable interface {
	Metadata(ctx context.Context) (*bigquery.TableMetadata, error)
	Create(ctx context.Context, meta *bigquery.TableMetadata) error
	Delete(ctx context.Context) error
}

// BQDataset is our abstraction of a BigQuery dataset.
type BQDataset interface {
	Metadata(ctx context.Context) (*bigquery.DatasetMetadata, error)
	Create(ctx context.Context, meta *bigquery.DatasetMetadata) error
	Update(ctx context.Context, metaToUpdate bigquery.DatasetMetadataToUpdate, etag string) (*bigquery.DatasetMetadata, error)
	Delete(ctx context.Context) error
	Table(tableID string) BQTable
	// DeleteWithContents recursively deletes all tables in the dataset, then the dataset itself.
	DeleteWithContents(ctx context.Context) error
}

// BQClient is our abstraction of the top-level BigQuery client.
type BQClient interface {
	Dataset(datasetID string) BQDataset
	Project() string
	Close() error
}

// --- Adapters for the real BigQuery client ---

// bqTableClientAdapter wraps a real *bigquery.Table to satisfy the BQTable interface.
type bqTableClientAdapter struct{ table *bigquery.Table }

func (a *bqTableClientAdapter) Metadata(ctx context.Context) (*bigquery.TableMetadata, error) {
	return a.table.Metadata(ctx)
}
func (a *bqTableClientAdapter) Create(ctx context.Context, meta *bigquery.TableMetadata) error {
	return a.table.Create(ctx, meta)
}
func (a *bqTableClientAdapter) Delete(ctx context.Context) error {
	return a.table.Delete(ctx)
}

// bqDatasetClientAdapter wraps a real *bigquery.Dataset to satisfy the BQDataset interface.
type bqDatasetClientAdapter struct{ dataset *bigquery.Dataset }

func (a *bqDatasetClientAdapter) Metadata(ctx context.Context) (*bigquery.DatasetMetadata, error) {
	return a.dataset.Metadata(ctx)
}
func (a *bqDatasetClientAdapter) Create(ctx context.Context, meta *bigquery.DatasetMetadata) error {
	return a.dataset.Create(ctx, meta)
}
func (a *bqDatasetClientAdapter) Update(ctx context.Context, metaToUpdate bigquery.DatasetMetadataToUpdate, etag string) (*bigquery.DatasetMetadata, error) {
	return a.dataset.Update(ctx, metaToUpdate, etag)
}
func (a *bqDatasetClientAdapter) Table(tableID string) BQTable {
	return &bqTableClientAdapter{table: a.dataset.Table(tableID)}
}
func (a *bqDatasetClientAdapter) Delete(ctx context.Context) error {
	return a.dataset.Delete(ctx)
}
func (a *bqDatasetClientAdapter) DeleteWithContents(ctx context.Context) error {
	return a.dataset.DeleteWithContents(ctx)
}

// bqClientAdapter wraps a real *bigquery.Client to satisfy the BQClient interface.
type bqClientAdapter struct{ client *bigquery.Client }

func (a *bqClientAdapter) Dataset(datasetID string) BQDataset {
	return &bqDatasetClientAdapter{dataset: a.client.Dataset(datasetID)}
}
func (a *bqClientAdapter) Project() string {
	return a.client.Project()
}
func (a *bqClientAdapter) Close() error {
	return a.client.Close()
}

// CreateGoogleBigQueryClient creates a real BigQuery client for use in production.
func CreateGoogleBigQueryClient(ctx context.Context, projectID string, clientOpts ...option.ClientOption) (BQClient, error) {
	realClient, err := bigquery.NewClient(ctx, projectID, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("bigquery.NewClient: %w", err)
	}
	return &bqClientAdapter{client: realClient}, nil
}

// isNotFound checks if an error is a Google Cloud API "not found" error.
func isNotFound(err error) bool {
	var e *googleapi.Error
	if errors.As(err, &e) {
		return e.Code == http.StatusNotFound
	}
	return false
}

// --- BigQuery Manager ---

// BigQueryManager handles the creation, verification, and deletion of BigQuery datasets and tables.
type BigQueryManager struct {
	client      BQClient
	logger      zerolog.Logger
	environment Environment
}

// NewAdapter creates a new BQClient adapter from a concrete *bigquery.Client.
// This is useful for testing or wrapping an existing client.
func NewAdapter(client *bigquery.Client) BQClient {
	return &bqClientAdapter{client: client}
}

// NewBigQueryManager creates a new manager for orchestrating BigQuery resources.
func NewBigQueryManager(client BQClient, logger zerolog.Logger, environment Environment) (*BigQueryManager, error) {
	if client == nil {
		return nil, errors.New("BigQuery client (BQClient interface) cannot be nil")
	}
	return &BigQueryManager{
		client:      client,
		logger:      logger.With().Str("subcomponent", "BigQueryManager").Logger(),
		environment: environment,
	}, nil
}

// CreateResources creates all configured BigQuery datasets and tables concurrently based on the provided spec.
// It is idempotent; if a resource already exists, it will be skipped.
func (m *BigQueryManager) CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedBigQueryTable, []ProvisionedBigQueryDataset, error) {
	m.logger.Info().Msg("Starting BigQuery setup...")
	validationErr := m.Validate(resources)
	if validationErr != nil {
		return nil, nil, fmt.Errorf("BigQuery configuration validation failed: %w", validationErr)
	}

	var allErrors []error
	var provisionedTables []ProvisionedBigQueryTable
	var provisionedDatasets []ProvisionedBigQueryDataset
	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.BigQueryDatasets)+len(resources.BigQueryTables))
	provisionedDatasetChan := make(chan ProvisionedBigQueryDataset, len(resources.BigQueryDatasets))
	provisionedTableChan := make(chan ProvisionedBigQueryTable, len(resources.BigQueryTables))

	// Create Datasets concurrently.
	for _, dsCfg := range resources.BigQueryDatasets {
		wg.Add(1)
		go func(dsCfg BigQueryDataset) {
			defer wg.Done()
			log := m.logger.With().Str("dataset", dsCfg.Name).Logger()
			log.Info().Msg("Processing dataset...")

			dataset := m.client.Dataset(dsCfg.Name)
			_, err := dataset.Metadata(ctx)
			if err == nil {
				log.Info().Msg("Dataset already exists, skipping creation.")
				provisionedDatasetChan <- ProvisionedBigQueryDataset{Name: dsCfg.Name}
				return
			}
			if !isNotFound(err) {
				errChan <- fmt.Errorf("failed to check existence of dataset '%s': %w", dsCfg.Name, err)
				return
			}

			log.Info().Msg("Creating dataset...")
			createErr := dataset.Create(ctx, &bigquery.DatasetMetadata{
				Labels:   dsCfg.Labels,
				Location: dsCfg.Location,
			})
			if createErr != nil {
				errChan <- fmt.Errorf("failed to create dataset '%s': %w", dsCfg.Name, createErr)
			} else {
				log.Info().Msg("Dataset created successfully.")
				provisionedDatasetChan <- ProvisionedBigQueryDataset{Name: dsCfg.Name}
			}
		}(dsCfg)
	}
	wg.Wait()
	close(provisionedDatasetChan)
	for pd := range provisionedDatasetChan {
		provisionedDatasets = append(provisionedDatasets, pd)
	}

	// Create Tables concurrently, now that datasets are likely to exist.
	for _, tableCfg := range resources.BigQueryTables {
		wg.Add(1)
		go func(tableCfg BigQueryTable) {
			defer wg.Done()
			log := m.logger.With().Str("dataset", tableCfg.Dataset).Str("table", tableCfg.Name).Logger()
			log.Info().Msg("Processing table...")
			table := m.client.Dataset(tableCfg.Dataset).Table(tableCfg.Name)
			_, err := table.Metadata(ctx)
			if err == nil {
				log.Info().Msg("Table already exists, skipping creation.")
				provisionedTableChan <- ProvisionedBigQueryTable{Dataset: tableCfg.Dataset, Name: tableCfg.Name}
				return
			}
			if !isNotFound(err) {
				errChan <- fmt.Errorf("failed to check existence of table '%s' in dataset '%s': %w", tableCfg.Name, tableCfg.Dataset, err)
				return
			}
			schemaStruct, _ := registeredSchemas[tableCfg.SchemaType]
			bqSchema, inferErr := bigquery.InferSchema(schemaStruct)
			if inferErr != nil {
				errChan <- fmt.Errorf("failed to infer BigQuery schema for type '%s': %w", tableCfg.SchemaType, inferErr)
				return
			}

			meta := &bigquery.TableMetadata{
				Schema: bqSchema,
				TimePartitioning: &bigquery.TimePartitioning{
					Field: tableCfg.TimePartitioningField,
					Type:  bigquery.TimePartitioningType(tableCfg.TimePartitioningType),
				},
				Labels: tableCfg.Labels,
			}
			if len(tableCfg.ClusteringFields) > 0 {
				meta.Clustering = &bigquery.Clustering{
					Fields: tableCfg.ClusteringFields,
				}
			}

			log.Info().Msg("Creating table...")
			createErr := table.Create(ctx, meta)
			if createErr != nil {
				errChan <- fmt.Errorf("failed to create table '%s' in dataset '%s': %w", tableCfg.Name, tableCfg.Dataset, createErr)
			} else {
				log.Info().Msg("Table created successfully.")
				provisionedTableChan <- ProvisionedBigQueryTable{Dataset: tableCfg.Dataset, Name: tableCfg.Name}
			}
		}(tableCfg)
	}
	wg.Wait()
	close(provisionedTableChan)
	close(errChan)

	for pt := range provisionedTableChan {
		provisionedTables = append(provisionedTables, pt)
	}
	for err := range errChan {
		allErrors = append(allErrors, err)
		m.logger.Error().Err(err).Msg("An error occurred during resource creation.")
	}

	if len(allErrors) > 0 {
		return provisionedTables, provisionedDatasets, errors.Join(allErrors...)
	}

	m.logger.Info().Msg("BigQuery setup completed successfully.")
	return provisionedTables, provisionedDatasets, nil
}

// Validate performs pre-flight checks on the BigQuery resource configuration.
// It ensures that all specified schema types have been registered with the manager.
func (m *BigQueryManager) Validate(resources CloudResourcesSpec) error {
	m.logger.Info().Msg("Validating BigQuery resource configuration...")
	var allErrors []error

	for _, dsCfg := range resources.BigQueryDatasets {
		if dsCfg.Name == "" {
			allErrors = append(allErrors, errors.New("dataset configuration found with an empty name"))
		}
	}

	for _, tableCfg := range resources.BigQueryTables {
		if tableCfg.Name == "" || tableCfg.Dataset == "" {
			allErrors = append(allErrors, fmt.Errorf("table configuration for '%s' has an empty name or dataset", tableCfg.Name))
		}

		registryMu.RLock()
		_, ok := registeredSchemas[tableCfg.SchemaType]
		registryMu.RUnlock()

		if !ok {
			allErrors = append(allErrors, fmt.Errorf("schema type '%s' for table '%s' not found in registry", tableCfg.SchemaType, tableCfg.Name))
		}
	}

	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}

	m.logger.Info().Msg("BigQuery resource configuration is valid.")
	return nil
}

// Verify checks if all specified BigQuery datasets and tables exist.
func (m *BigQueryManager) Verify(ctx context.Context, resources CloudResourcesSpec) error {
	m.logger.Info().Msg("Verifying BigQuery resources...")
	var allErrors []error

	datasetErr := m.VerifyDatasets(ctx, resources.BigQueryDatasets)
	if datasetErr != nil {
		allErrors = append(allErrors, datasetErr)
	}

	tableErr := m.VerifyTables(ctx, resources.BigQueryTables)
	if tableErr != nil {
		allErrors = append(allErrors, tableErr)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("BigQuery verification failed: %w", errors.Join(allErrors...))
	}

	m.logger.Info().Msg("BigQuery verification completed successfully.")
	return nil
}

// VerifyDatasets checks if the specified BigQuery datasets exist.
func (m *BigQueryManager) VerifyDatasets(ctx context.Context, datasetsToVerify []BigQueryDataset) error {
	m.logger.Info().Int("count", len(datasetsToVerify)).Msg("Verifying BigQuery datasets...")
	var allErrors []error
	for _, dsCfg := range datasetsToVerify {
		if dsCfg.Name == "" {
			continue
		}
		_, err := m.client.Dataset(dsCfg.Name).Metadata(ctx)
		if err != nil {
			if isNotFound(err) {
				allErrors = append(allErrors, fmt.Errorf("dataset '%s' not found", dsCfg.Name))
			} else {
				allErrors = append(allErrors, fmt.Errorf("failed to check dataset '%s': %w", dsCfg.Name, err))
			}
			continue
		}
		m.logger.Debug().Str("dataset", dsCfg.Name).Msg("Dataset verified successfully.")
	}
	return errors.Join(allErrors...)
}

// VerifyTables checks if the specified BigQuery tables exist.
func (m *BigQueryManager) VerifyTables(ctx context.Context, tablesToVerify []BigQueryTable) error {
	m.logger.Info().Int("count", len(tablesToVerify)).Msg("Verifying BigQuery tables...")
	var allErrors []error
	for _, tableCfg := range tablesToVerify {
		if tableCfg.Name == "" || tableCfg.Dataset == "" {
			continue
		}
		_, err := m.client.Dataset(tableCfg.Dataset).Table(tableCfg.Name).Metadata(ctx)
		if err != nil {
			if isNotFound(err) {
				allErrors = append(allErrors, fmt.Errorf("table '%s' in dataset '%s' not found", tableCfg.Name, tableCfg.Dataset))
			} else {
				allErrors = append(allErrors, fmt.Errorf("failed to check table '%s' in dataset '%s': %w", tableCfg.Name, tableCfg.Dataset, err))
			}
			continue
		}
		m.logger.Debug().Str("table", tableCfg.Name).Str("dataset", tableCfg.Dataset).Msg("Table verified successfully.")
	}
	return errors.Join(allErrors...)
}

// Teardown deletes all configured BigQuery resources by deleting the parent datasets with their contents.
// This is more efficient than deleting each table individually.
func (m *BigQueryManager) Teardown(ctx context.Context, resources CloudResourcesSpec) error {
	m.logger.Info().Msg("Starting BigQuery teardown...")
	var allErrors []error
	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.BigQueryDatasets))

	m.logger.Info().Int("count", len(resources.BigQueryDatasets)).Msg("Tearing down BigQuery datasets...")
	for _, dsCfg := range resources.BigQueryDatasets {
		wg.Add(1)
		go func(dsCfg BigQueryDataset) {
			defer wg.Done()
			log := m.logger.With().Str("dataset", dsCfg.Name).Logger()
			if dsCfg.TeardownProtection {
				log.Warn().Msg("Teardown protection enabled, skipping deletion.")
				return
			}
			dataset := m.client.Dataset(dsCfg.Name)
			log.Info().Msg("Attempting to delete dataset with its contents...")

			// This single call deletes the dataset and all tables within it.
			err := dataset.DeleteWithContents(ctx)
			if err != nil {
				if isNotFound(err) {
					log.Info().Msg("Dataset not found, skipping deletion.")
				} else {
					errChan <- fmt.Errorf("failed to delete dataset %s: %w", dsCfg.Name, err)
				}
			} else {
				log.Info().Msg("Dataset and its contents deleted successfully.")
			}
		}(dsCfg)
	}
	wg.Wait()
	close(errChan)

	for err := range errChan {
		allErrors = append(allErrors, err)
		m.logger.Error().Err(err).Msg("An error occurred during teardown.")
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("BigQuery teardown completed with errors: %w", errors.Join(allErrors...))
	}

	m.logger.Info().Msg("BigQuery teardown completed successfully.")
	return nil
}
