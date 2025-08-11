package servicemanager_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- Mocks for Firestore Adapters ---

type MockDatabaseHandle struct{ mock.Mock }

func (m *MockDatabaseHandle) Exists(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}
func (m *MockDatabaseHandle) Create(ctx context.Context, locationID string, dbType string) error {
	args := m.Called(ctx, locationID, dbType)
	return args.Error(0)
}
func (m *MockDatabaseHandle) Delete(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type MockDocumentStoreClient struct{ mock.Mock }

func (m *MockDocumentStoreClient) Database(dbID string) servicemanager.DatabaseHandle {
	args := m.Called(dbID)
	return args.Get(0).(servicemanager.DatabaseHandle)
}
func (m *MockDocumentStoreClient) Close() error {
	args := m.Called()
	return args.Error(0)
}

// --- Test Setup ---

func setupFirestoreManagerTest(t *testing.T) (*servicemanager.FirestoreManager, *MockDocumentStoreClient) {
	mockClient := new(MockDocumentStoreClient)
	logger := zerolog.Nop()
	env := servicemanager.Environment{Name: "test-env", ProjectID: "test-project"}

	manager, err := servicemanager.NewFirestoreManager(mockClient, logger, env)
	require.NoError(t, err)
	require.NotNil(t, manager)

	return manager, mockClient
}

func getTestFirestoreResources() servicemanager.CloudResourcesSpec {
	return servicemanager.CloudResourcesSpec{
		FirestoreDatabases: []servicemanager.FirestoreDatabase{
			{
				CloudResource: servicemanager.CloudResource{Name: "test-db"},
				LocationID:    "nam5",
				Type:          servicemanager.FirestoreModeNative,
			},
		},
	}
}

// --- Tests ---

func TestFirestoreManager_CreateResources_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)
	manager, mockClient := setupFirestoreManagerTest(t)
	resources := getTestFirestoreResources()
	dbConfig := resources.FirestoreDatabases[0]
	const defaultDbID = "(default)"

	mockHandle := new(MockDatabaseHandle)
	mockClient.On("Database", defaultDbID).Return(mockHandle).Once()
	mockHandle.On("Exists", ctx).Return(false, nil).Once()
	mockHandle.On("Create", ctx, dbConfig.LocationID, string(dbConfig.Type)).Return(nil).Once()

	provisioned, err := manager.CreateResources(ctx, resources)

	assert.NoError(t, err)
	assert.Len(t, provisioned, 1)
	assert.Equal(t, "test-db", provisioned[0].Name)
	mockClient.AssertExpectations(t)
	mockHandle.AssertExpectations(t)
}

func TestFirestoreManager_CreateResources_AlreadyExists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)
	manager, mockClient := setupFirestoreManagerTest(t)
	resources := getTestFirestoreResources()
	const defaultDbID = "(default)"

	mockHandle := new(MockDatabaseHandle)
	mockClient.On("Database", defaultDbID).Return(mockHandle).Once()
	mockHandle.On("Exists", ctx).Return(true, nil).Once()

	provisioned, err := manager.CreateResources(ctx, resources)

	assert.NoError(t, err)
	assert.Len(t, provisioned, 1)
	mockHandle.AssertNotCalled(t, "Create", mock.Anything, mock.Anything, mock.Anything)
	mockClient.AssertExpectations(t)
	mockHandle.AssertExpectations(t)
}

func TestFirestoreManager_CreateResources_Failure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)
	manager, mockClient := setupFirestoreManagerTest(t)
	resources := getTestFirestoreResources()
	const defaultDbID = "(default)"
	creationError := errors.New("location not available")

	mockHandle := new(MockDatabaseHandle)
	mockClient.On("Database", defaultDbID).Return(mockHandle).Once()
	mockHandle.On("Exists", ctx).Return(false, nil).Once()
	mockHandle.On("Create", ctx, mock.Anything, mock.Anything).Return(creationError).Once()

	_, err := manager.CreateResources(ctx, resources)

	assert.Error(t, err)
	assert.ErrorContains(t, err, creationError.Error())
	mockClient.AssertExpectations(t)
	mockHandle.AssertExpectations(t)
}

func TestFirestoreManager_Teardown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)
	manager, _ := setupFirestoreManagerTest(t)
	resources := getTestFirestoreResources()

	// Teardown should do nothing for firestore and not return an error.
	err := manager.Teardown(ctx, resources)
	assert.NoError(t, err)
}

func TestFirestoreManager_Verify(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)
	manager, mockClient := setupFirestoreManagerTest(t)
	resources := getTestFirestoreResources()
	const defaultDbID = "(default)"

	t.Run("Success", func(t *testing.T) {
		mockHandle := new(MockDatabaseHandle)
		mockClient.On("Database", defaultDbID).Return(mockHandle).Once()
		mockHandle.On("Exists", ctx).Return(true, nil).Once()

		err := manager.Verify(ctx, resources)
		assert.NoError(t, err)
		mockHandle.AssertExpectations(t)
	})

	t.Run("Failure", func(t *testing.T) {
		mockHandle := new(MockDatabaseHandle)
		mockClient.On("Database", defaultDbID).Return(mockHandle).Once()
		mockHandle.On("Exists", ctx).Return(false, nil).Once()

		err := manager.Verify(ctx, resources)
		assert.Error(t, err)
		assert.ErrorContains(t, err, "firestore database 'test-db' not found")
		mockHandle.AssertExpectations(t)
	})
}
