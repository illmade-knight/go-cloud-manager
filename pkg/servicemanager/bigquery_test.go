package servicemanager_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"cloud.google.com/go/bigquery"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/api/googleapi"
)

// --- Mocks ---

type MockBQTable struct{ mock.Mock }

func (m *MockBQTable) Metadata(ctx context.Context) (*bigquery.TableMetadata, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*bigquery.TableMetadata), args.Error(1)
}
func (m *MockBQTable) Create(ctx context.Context, meta *bigquery.TableMetadata) error {
	args := m.Called(ctx, meta)
	return args.Error(0)
}
func (m *MockBQTable) Delete(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type MockBQDataset struct{ mock.Mock }

func (m *MockBQDataset) Metadata(ctx context.Context) (*bigquery.DatasetMetadata, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*bigquery.DatasetMetadata), args.Error(1)
}
func (m *MockBQDataset) Create(ctx context.Context, meta *bigquery.DatasetMetadata) error {
	args := m.Called(ctx, meta)
	return args.Error(0)
}
func (m *MockBQDataset) Update(ctx context.Context, metaToUpdate bigquery.DatasetMetadataToUpdate, etag string) (*bigquery.DatasetMetadata, error) {
	args := m.Called(ctx, metaToUpdate, etag)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*bigquery.DatasetMetadata), args.Error(1)
}
func (m *MockBQDataset) Table(tableID string) servicemanager.BQTable {
	args := m.Called(tableID)
	return args.Get(0).(servicemanager.BQTable)
}
func (m *MockBQDataset) Delete(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}
func (m *MockBQDataset) DeleteWithContents(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type MockBQClient struct{ mock.Mock }

func (m *MockBQClient) Dataset(datasetID string) servicemanager.BQDataset {
	args := m.Called(datasetID)
	return args.Get(0).(servicemanager.BQDataset)
}
func (m *MockBQClient) Project() string {
	args := m.Called()
	return args.String(0)
}
func (m *MockBQClient) Close() error {
	args := m.Called()
	return args.Error(0)
}

// --- Test Setup ---

// TestSchema is a sample schema for testing.
type TestSchema struct {
	Field1 string `bigquery:"field1"`
	Field2 int64  `bigquery:"field2"`
}

func setupBigQueryManagerTest(t *testing.T) (*servicemanager.BigQueryManager, *MockBQClient) {
	mockClient := new(MockBQClient)
	logger := zerolog.Nop()
	schemaRegistry := map[string]interface{}{
		"testSchemaV1": TestSchema{},
	}
	environment := servicemanager.Environment{Name: "test"}

	manager, err := servicemanager.NewBigQueryManager(mockClient, logger, schemaRegistry, environment)
	assert.NoError(t, err)
	assert.NotNil(t, manager)

	return manager, mockClient
}

// getTestBigQueryResources correctly initializes the embedded CloudResource struct.
func getTestBigQueryResources() servicemanager.CloudResourcesSpec {
	return servicemanager.CloudResourcesSpec{
		BigQueryDatasets: []servicemanager.BigQueryDataset{
			{CloudResource: servicemanager.CloudResource{Name: "test_dataset_1"}},
			{CloudResource: servicemanager.CloudResource{Name: "test_dataset_2"}},
		},
		BigQueryTables: []servicemanager.BigQueryTable{
			{
				CloudResource:    servicemanager.CloudResource{Name: "test_table_1"},
				Dataset:          "test_dataset_1",
				SchemaImportPath: "testSchemaV1",
			},
			{
				CloudResource:    servicemanager.CloudResource{Name: "test_table_2"},
				Dataset:          "test_dataset_2",
				SchemaImportPath: "testSchemaV1",
			},
		},
	}
}

// --- Tests ---

func TestNewBigQueryManager(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		setupBigQueryManagerTest(t)
	})

	t.Run("Nil Client", func(t *testing.T) {
		_, err := servicemanager.NewBigQueryManager(nil, zerolog.Nop(), map[string]interface{}{}, servicemanager.Environment{})
		assert.Error(t, err)
		assert.Equal(t, "BigQuery client (BQClient interface) cannot be nil", err.Error())
	})
}

func TestBigQueryManager_Validate(t *testing.T) {
	manager, _ := setupBigQueryManagerTest(t)

	t.Run("Success", func(t *testing.T) {
		resources := getTestBigQueryResources()
		err := manager.Validate(resources)
		assert.NoError(t, err)
	})

	t.Run("Invalid Table Schema", func(t *testing.T) {
		resources := getTestBigQueryResources()
		resources.BigQueryTables = append(resources.BigQueryTables, servicemanager.BigQueryTable{
			CloudResource:    servicemanager.CloudResource{Name: "bad_table"},
			Dataset:          "test_dataset_1",
			SchemaImportPath: "nonExistentSchema",
		})
		err := manager.Validate(resources)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "schema 'nonExistentSchema' for table 'bad_table' not found in registry")
	})
}

func TestBigQueryManager_CreateResources(t *testing.T) {
	ctx := context.Background()
	notFoundErr := &googleapi.Error{Code: http.StatusNotFound, Message: "not found"}

	t.Run("Success - Create All", func(t *testing.T) {
		manager, mockClient := setupBigQueryManagerTest(t)
		resources := getTestBigQueryResources()

		mockDs1, mockDs2 := new(MockBQDataset), new(MockBQDataset)
		mockClient.On("Dataset", "test_dataset_1").Return(mockDs1)
		mockClient.On("Dataset", "test_dataset_2").Return(mockDs2)
		mockDs1.On("Metadata", ctx).Return(nil, notFoundErr).Once()
		mockDs1.On("Create", ctx, mock.Anything).Return(nil).Once()
		mockDs2.On("Metadata", ctx).Return(nil, notFoundErr).Once()
		mockDs2.On("Create", ctx, mock.Anything).Return(nil).Once()

		mockTbl1, mockTbl2 := new(MockBQTable), new(MockBQTable)
		mockDs1.On("Table", "test_table_1").Return(mockTbl1).Once()
		mockTbl1.On("Metadata", ctx).Return(nil, notFoundErr).Once()
		mockTbl1.On("Create", ctx, mock.AnythingOfType("*bigquery.TableMetadata")).Return(nil).Once()
		mockDs2.On("Table", "test_table_2").Return(mockTbl2).Once()
		mockTbl2.On("Metadata", ctx).Return(nil, notFoundErr).Once()
		mockTbl2.On("Create", ctx, mock.AnythingOfType("*bigquery.TableMetadata")).Return(nil).Once()

		_, _, err := manager.CreateResources(ctx, resources)

		assert.NoError(t, err)
		mockClient.AssertExpectations(t)
	})

	t.Run("Partial Success - One Table Fails to Create", func(t *testing.T) {
		manager, mockClient := setupBigQueryManagerTest(t)
		resources := getTestBigQueryResources()
		creationErr := errors.New("API limit exceeded")

		mockDs1, mockDs2 := new(MockBQDataset), new(MockBQDataset)
		mockClient.On("Dataset", "test_dataset_1").Return(mockDs1)
		mockClient.On("Dataset", "test_dataset_2").Return(mockDs2)
		mockDs1.On("Metadata", ctx).Return(nil, notFoundErr).Once()
		mockDs1.On("Create", ctx, mock.Anything).Return(nil).Once()
		mockDs2.On("Metadata", ctx).Return(nil, notFoundErr).Once()
		mockDs2.On("Create", ctx, mock.Anything).Return(nil).Once()

		mockTbl1, mockTbl2 := new(MockBQTable), new(MockBQTable)
		mockDs1.On("Table", "test_table_1").Return(mockTbl1).Once()
		mockTbl1.On("Metadata", ctx).Return(nil, notFoundErr).Once()
		mockTbl1.On("Create", ctx, mock.Anything).Return(nil).Once() // Succeeds
		mockDs2.On("Table", "test_table_2").Return(mockTbl2).Once()
		mockTbl2.On("Metadata", ctx).Return(nil, notFoundErr).Once()
		mockTbl2.On("Create", ctx, mock.Anything).Return(creationErr).Once() // Fails

		_, _, err := manager.CreateResources(ctx, resources)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create table 'test_table_2' in dataset 'test_dataset_2'")
		assert.Contains(t, err.Error(), creationErr.Error())
		mockClient.AssertExpectations(t)
	})
}

func TestBigQueryManager_Teardown(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Delete All", func(t *testing.T) {
		manager, mockClient := setupBigQueryManagerTest(t)
		resources := getTestBigQueryResources()

		mockDs1, mockDs2 := new(MockBQDataset), new(MockBQDataset)
		mockClient.On("Dataset", "test_dataset_1").Return(mockDs1)
		mockClient.On("Dataset", "test_dataset_2").Return(mockDs2)
		mockDs1.On("DeleteWithContents", ctx).Return(nil).Once()
		mockDs2.On("DeleteWithContents", ctx).Return(nil).Once()

		mockTbl1, mockTbl2 := new(MockBQTable), new(MockBQTable)
		mockDs1.On("Table", "test_table_1").Return(mockTbl1).Once()
		mockTbl1.On("Delete", ctx).Return(nil).Once()
		mockDs2.On("Table", "test_table_2").Return(mockTbl2).Once()
		mockTbl2.On("Delete", ctx).Return(nil).Once()

		err := manager.Teardown(ctx, resources)
		assert.NoError(t, err)
		mockClient.AssertExpectations(t)
	})

	t.Run("Teardown Protection Enabled", func(t *testing.T) {
		manager, mockClient := setupBigQueryManagerTest(t)
		resources := getTestBigQueryResources()
		// Correctly set the teardown protection on the embedded struct
		resources.BigQueryDatasets[0].CloudResource.TeardownProtection = true
		resources.BigQueryTables[1].CloudResource.TeardownProtection = true

		mockDs1, mockDs2 := new(MockBQDataset), new(MockBQDataset)
		mockClient.On("Dataset", "test_dataset_1").Return(mockDs1)
		mockClient.On("Dataset", "test_dataset_2").Return(mockDs2)

		mockTbl1, mockTbl2 := new(MockBQTable), new(MockBQTable)
		mockDs1.On("Table", "test_table_1").Return(mockTbl1).Once()
		mockDs2.On("Table", "test_table_2").Return(mockTbl2).Once()

		// Expect deletion ONLY on unprotected resources
		mockDs2.On("DeleteWithContents", ctx).Return(nil).Once() // ds2 is not protected
		mockTbl1.On("Delete", ctx).Return(nil).Once()            // tbl1 is not protected

		err := manager.Teardown(ctx, resources)

		assert.NoError(t, err)
		mockClient.AssertExpectations(t)
		// Assert that deletion was NOT called on protected resources
		mockDs1.AssertNotCalled(t, "DeleteWithContents", ctx)
		mockTbl2.AssertNotCalled(t, "Delete", ctx)
	})
}
