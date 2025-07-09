package servicemanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// --- Mocks for Manager Interfaces ---

type MockMessagingManager struct{ mock.Mock }

func (m *MockMessagingManager) CreateResources(ctx context.Context, resources servicemanager.CloudResourcesSpec) ([]servicemanager.ProvisionedTopic, []servicemanager.ProvisionedSubscription, error) {
	args := m.Called(ctx, resources)
	return args.Get(0).([]servicemanager.ProvisionedTopic), args.Get(1).([]servicemanager.ProvisionedSubscription), args.Error(2)
}
func (m *MockMessagingManager) Teardown(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}
func (m *MockMessagingManager) Verify(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}

type MockStorageManager struct{ mock.Mock }

func (m *MockStorageManager) CreateResources(ctx context.Context, resources servicemanager.CloudResourcesSpec) ([]servicemanager.ProvisionedGCSBucket, error) {
	args := m.Called(ctx, resources)
	return args.Get(0).([]servicemanager.ProvisionedGCSBucket), args.Error(1)
}
func (m *MockStorageManager) Teardown(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}
func (m *MockStorageManager) Verify(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}

type MockBigQueryManager struct{ mock.Mock }

func (m *MockBigQueryManager) CreateResources(ctx context.Context, resources servicemanager.CloudResourcesSpec) ([]servicemanager.ProvisionedBigQueryTable, []servicemanager.ProvisionedBigQueryDataset, error) {
	args := m.Called(ctx, resources)
	return args.Get(0).([]servicemanager.ProvisionedBigQueryTable), args.Get(1).([]servicemanager.ProvisionedBigQueryDataset), args.Error(2)
}
func (m *MockBigQueryManager) Teardown(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}
func (m *MockBigQueryManager) Verify(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}

// --- Test Setup ---

func setupDataflowManagerTest(t *testing.T) (*servicemanager.DataflowManager, *MockMessagingManager, *MockStorageManager, *MockBigQueryManager) {
	mockMsg := new(MockMessagingManager)
	mockStore := new(MockStorageManager)
	mockBq := new(MockBigQueryManager)
	logger := zerolog.Nop()
	env := servicemanager.Environment{}

	manager, err := servicemanager.NewDataflowManagerFromManagers(mockMsg, mockStore, mockBq, env, logger)
	assert.NoError(t, err)
	assert.NotNil(t, manager)

	return manager, mockMsg, mockStore, mockBq
}

// --- Tests ---

func TestDataflowManager_CreateResources_Success(t *testing.T) {
	manager, mockMsg, mockStore, mockBq := setupDataflowManagerTest(t)
	ctx := context.Background()
	resources := servicemanager.CloudResourcesSpec{}

	// Mock successful return values for all sub-managers
	mockMsg.On("CreateResources", ctx, resources).Return([]servicemanager.ProvisionedTopic{{Name: "t1"}}, []servicemanager.ProvisionedSubscription{{Name: "s1"}}, nil).Once()
	mockStore.On("CreateResources", ctx, resources).Return([]servicemanager.ProvisionedGCSBucket{{Name: "b1"}}, nil).Once()
	mockBq.On("CreateResources", ctx, resources).Return([]servicemanager.ProvisionedBigQueryTable{{Name: "tbl1"}}, []servicemanager.ProvisionedBigQueryDataset{{Name: "ds1"}}, nil).Once()

	// Action
	provisioned, err := manager.CreateResources(ctx, resources)

	// Assert
	assert.NoError(t, err)
	assert.NotNil(t, provisioned)
	assert.Len(t, provisioned.Topics, 1)
	assert.Len(t, provisioned.GCSBuckets, 1)
	assert.Len(t, provisioned.BigQueryDatasets, 1)
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
}

func TestDataflowManager_CreateResources_PartialFailure(t *testing.T) {
	manager, mockMsg, mockStore, mockBq := setupDataflowManagerTest(t)
	ctx := context.Background()
	resources := servicemanager.CloudResourcesSpec{}
	bqErr := errors.New("bigquery permission denied")

	// Mock two successes and one failure
	mockMsg.On("CreateResources", ctx, resources).Return([]servicemanager.ProvisionedTopic{}, []servicemanager.ProvisionedSubscription{}, nil).Once()
	mockStore.On("CreateResources", ctx, resources).Return([]servicemanager.ProvisionedGCSBucket{{Name: "b1"}}, nil).Once()
	mockBq.On("CreateResources", ctx, resources).Return([]servicemanager.ProvisionedBigQueryTable{}, []servicemanager.ProvisionedBigQueryDataset{}, bqErr).Once()

	// Action
	provisioned, err := manager.CreateResources(ctx, resources)

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bigquery setup failed")
	assert.Contains(t, err.Error(), bqErr.Error())
	// Assert that partial results are still returned
	assert.NotNil(t, provisioned)
	assert.Len(t, provisioned.GCSBuckets, 1)
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
}

func TestDataflowManager_Teardown_Success(t *testing.T) {
	manager, mockMsg, mockStore, mockBq := setupDataflowManagerTest(t)
	ctx := context.Background()
	resources := servicemanager.CloudResourcesSpec{}

	// Mock successful teardown for all
	mockMsg.On("Teardown", ctx, resources).Return(nil).Once()
	mockStore.On("Teardown", ctx, resources).Return(nil).Once()
	mockBq.On("Teardown", ctx, resources).Return(nil).Once()

	// Action
	err := manager.TeardownResources(ctx, resources)

	// Assert
	assert.NoError(t, err)
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
}

func TestDataflowManager_Teardown_Failure(t *testing.T) {
	manager, mockMsg, mockStore, mockBq := setupDataflowManagerTest(t)
	ctx := context.Background()
	resources := servicemanager.CloudResourcesSpec{}
	teardownErr := errors.New("bucket not empty")

	// Mock one failure
	mockMsg.On("Teardown", ctx, resources).Return(nil).Once()
	mockStore.On("Teardown", ctx, resources).Return(teardownErr).Once()
	mockBq.On("Teardown", ctx, resources).Return(nil).Once()

	// Action
	err := manager.TeardownResources(ctx, resources)

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GCS teardown failed")
	assert.Contains(t, err.Error(), teardownErr.Error())
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
}

func TestDataflowManager_Verify_Success(t *testing.T) {
	manager, mockMsg, mockStore, mockBq := setupDataflowManagerTest(t)
	ctx := context.Background()
	resources := servicemanager.CloudResourcesSpec{}

	// Mock successful verification for all
	mockMsg.On("Verify", ctx, resources).Return(nil).Once()
	mockStore.On("Verify", ctx, resources).Return(nil).Once()
	mockBq.On("Verify", ctx, resources).Return(nil).Once()

	// Action
	err := manager.Verify(ctx, resources)

	// Assert
	assert.NoError(t, err)
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
}
