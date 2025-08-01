package servicemanager_test

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"testing"
	"time"

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

// TestMain is run once for the entire package before any other tests are run.
// It handles global setup, like registering schemas for the tests.
func TestMain(m *testing.M) {
	// One-time setup: Register the schema so it's available for all tests.
	servicemanager.RegisterSchema("testSchemaV1", TestSchema{})
	log.Println("Test schema registered globally for all tests in the servicemanager_test package.")

	// Run all tests in the package.
	exitCode := m.Run()

	// One-time teardown (if needed).
	os.Exit(exitCode)
}

func setupBigQueryManagerTest(t *testing.T) (*servicemanager.BigQueryManager, *MockBQClient) {
	mockClient := new(MockBQClient)
	logger := zerolog.Nop()
	environment := servicemanager.Environment{Name: "test"}

	manager, err := servicemanager.NewBigQueryManager(mockClient, logger, environment)
	assert.NoError(t, err)
	assert.NotNil(t, manager)

	return manager, mockClient
}

// getTestBigQueryResources provides a consistent set of test resources.
func getTestBigQueryResources() servicemanager.CloudResourcesSpec {
	return servicemanager.CloudResourcesSpec{
		BigQueryDatasets: []servicemanager.BigQueryDataset{
			{CloudResource: servicemanager.CloudResource{Name: "test_dataset_1"}},
			{CloudResource: servicemanager.CloudResource{Name: "test_dataset_2"}},
		},
		BigQueryTables: []servicemanager.BigQueryTable{
			{
				CloudResource: servicemanager.CloudResource{Name: "test_table_1"},
				Dataset:       "test_dataset_1",
				SchemaType:    "testSchemaV1",
			},
			{
				CloudResource: servicemanager.CloudResource{Name: "test_table_2"},
				Dataset:       "test_dataset_2",
				SchemaType:    "testSchemaV1",
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
		_, err := servicemanager.NewBigQueryManager(nil, zerolog.Nop(), servicemanager.Environment{})
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
			CloudResource: servicemanager.CloudResource{Name: "bad_table"},
			Dataset:       "test_dataset_1",
			SchemaType:    "nonExistentSchema",
		})
		err := manager.Validate(resources)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "schema type 'nonExistentSchema' for table 'bad_table' not found in registry")
	})
}

func TestBigQueryManager_CreateResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()
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
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	t.Run("Success - Delete All", func(t *testing.T) {
		manager, mockClient := setupBigQueryManagerTest(t)
		resources := getTestBigQueryResources()

		mockDs1, mockDs2 := new(MockBQDataset), new(MockBQDataset)
		mockClient.On("Dataset", "test_dataset_1").Return(mockDs1).Once()
		mockClient.On("Dataset", "test_dataset_2").Return(mockDs2).Once()

		// The implementation ONLY deletes the dataset with contents.
		// It does not and should not delete tables individually.
		mockDs1.On("DeleteWithContents", ctx).Return(nil).Once()
		mockDs2.On("DeleteWithContents", ctx).Return(nil).Once()

		err := manager.Teardown(ctx, resources)
		assert.NoError(t, err)

		// Assert that the mocks were called as expected.
		mockClient.AssertExpectations(t)
		mockDs1.AssertExpectations(t)
		mockDs2.AssertExpectations(t)
	})

	t.Run("Teardown Protection Enabled", func(t *testing.T) {
		manager, mockClient := setupBigQueryManagerTest(t)
		resources := getTestBigQueryResources()
		// Protect the first dataset. The tables within it are implicitly protected.
		resources.BigQueryDatasets[0].CloudResource.TeardownProtection = true

		mockDs2 := new(MockBQDataset)
		// Expect client call only for the unprotected dataset.
		mockClient.On("Dataset", "test_dataset_2").Return(mockDs2).Once()
		// Expect DeleteWithContents to be called only on the unprotected dataset.
		mockDs2.On("DeleteWithContents", ctx).Return(nil).Once()

		err := manager.Teardown(ctx, resources)
		assert.NoError(t, err)

		// Verify that the client was never asked for the protected dataset and that no
		// deletion was attempted on it.
		mockClient.AssertNotCalled(t, "Dataset", "test_dataset_1")
		mockClient.AssertExpectations(t)
		mockDs2.AssertExpectations(t)
	})
}
