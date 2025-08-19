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

// setupServiceManagerTest creates a ServiceManager with mock sub-managers for testing.
// REFACTOR_NOTE: Updated to include a mock Firestore manager and resources.
func setupServiceManagerTest(t *testing.T) (*servicemanager.ServiceManager, *MockMessagingManager, *MockStorageManager, *MockBigQueryManager, *MockFirestoreManager, *servicemanager.MicroserviceArchitecture) {
	mockMsg := new(MockMessagingManager)
	mockStore := new(MockStorageManager)
	mockBq := new(MockBigQueryManager)
	mockFs := new(MockFirestoreManager)

	// Create a sample architecture with two dataflows, one ephemeral and one permanent.
	arch := &servicemanager.MicroserviceArchitecture{
		Dataflows: map[string]servicemanager.ResourceGroup{
			"dataflow1": {
				Name: "dataflow1",
				Resources: servicemanager.CloudResourcesSpec{
					Topics:             []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: "df1-topic"}}},
					FirestoreDatabases: []servicemanager.FirestoreDatabase{{CloudResource: servicemanager.CloudResource{Name: "df1-db"}}},
				},
				Lifecycle: &servicemanager.LifecyclePolicy{Strategy: servicemanager.LifecycleStrategyEphemeral},
			},
			"dataflow2": {
				Name: "dataflow2",
				Resources: servicemanager.CloudResourcesSpec{
					GCSBuckets: []servicemanager.GCSBucket{{CloudResource: servicemanager.CloudResource{Name: "df2-bucket"}}},
				},
				Lifecycle: &servicemanager.LifecyclePolicy{Strategy: servicemanager.LifecycleStrategyPermanent},
			},
		},
	}

	manager, err := servicemanager.NewServiceManagerFromManagers(mockMsg, mockStore, mockBq, mockFs, nil, nil, zerolog.Nop())
	require.NoError(t, err)
	require.NotNil(t, manager)

	return manager, mockMsg, mockStore, mockBq, mockFs, arch
}

func TestServiceManager_SetupAll(t *testing.T) {
	manager, mockMsg, mockStore, mockBq, mockFs, arch := setupServiceManagerTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)

	// Mock the sub-manager calls.
	// We expect CreateResources to be called for each of the two dataflows.
	mockMsg.On("CreateResources", ctx, mock.Anything).Return([]servicemanager.ProvisionedTopic{}, []servicemanager.ProvisionedSubscription{}, nil).Twice()
	mockStore.On("CreateResources", ctx, mock.Anything).Return([]servicemanager.ProvisionedGCSBucket{}, nil).Twice()
	mockBq.On("CreateResources", ctx, mock.Anything).Return([]servicemanager.ProvisionedBigQueryTable{}, []servicemanager.ProvisionedBigQueryDataset{}, nil).Twice()
	mockFs.On("CreateResources", ctx, mock.Anything).Return([]servicemanager.ProvisionedFirestoreDatabase{}, nil).Twice()

	// Action
	_, err := manager.SetupAll(ctx, arch)

	// Assert
	assert.NoError(t, err)
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
	mockFs.AssertExpectations(t)
}

func TestServiceManager_TeardownAll(t *testing.T) {
	manager, mockMsg, mockStore, mockBq, mockFs, arch := setupServiceManagerTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)

	// Mock the sub-manager calls. Teardown should only be called ONCE, for the ephemeral dataflow ("dataflow1").
	// The permanent "dataflow2" should be skipped.
	df1Resources := arch.Dataflows["dataflow1"].Resources
	mockMsg.On("Teardown", ctx, df1Resources).Return(nil).Once()
	mockStore.On("Teardown", ctx, df1Resources).Return(nil).Once()
	mockBq.On("Teardown", ctx, df1Resources).Return(nil).Once()
	mockFs.On("Teardown", ctx, df1Resources).Return(nil).Once()

	// Action
	err := manager.TeardownAll(ctx, arch)

	// Assert
	assert.NoError(t, err)
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
	mockFs.AssertExpectations(t)
}

func TestServiceManager_SetupDataflow_Failure(t *testing.T) {
	manager, mockMsg, mockStore, mockBq, mockFs, arch := setupServiceManagerTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)
	storageErr := errors.New("bucket already exists and is owned by another project")

	// Mock two successes and one failure for the "dataflow1" call.
	df1Resources := arch.Dataflows["dataflow1"].Resources
	mockMsg.On("CreateResources", ctx, df1Resources).Return([]servicemanager.ProvisionedTopic{}, []servicemanager.ProvisionedSubscription{}, nil).Once()
	mockStore.On("CreateResources", ctx, df1Resources).Return([]servicemanager.ProvisionedGCSBucket{}, storageErr).Once() // This one fails.
	mockBq.On("CreateResources", ctx, df1Resources).Return([]servicemanager.ProvisionedBigQueryTable{}, []servicemanager.ProvisionedBigQueryDataset{}, nil).Once()
	mockFs.On("CreateResources", ctx, df1Resources).Return([]servicemanager.ProvisionedFirestoreDatabase{}, nil).Once()

	// Action
	_, err := manager.SetupFoundationalDataflow(ctx, arch, "dataflow1")

	// Assert
	assert.Error(t, err)
	assert.ErrorContains(t, err, storageErr.Error())
	// Verify all managers were called, even with the error, due to concurrency.
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
	mockFs.AssertExpectations(t)
}

func TestServiceManager_TeardownDataflow_Skipped(t *testing.T) {
	manager, mockMsg, mockStore, mockBq, mockFs, arch := setupServiceManagerTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	t.Cleanup(cancel)

	// Action: Try to tear down "dataflow2", which is marked as permanent.
	err := manager.TeardownDataflow(ctx, arch, "dataflow2")

	// Assert
	assert.NoError(t, err, "Should not return an error when skipping a non-ephemeral teardown")
	// Assert that NO teardown methods were called on any sub-manager.
	mockMsg.AssertNotCalled(t, "Teardown", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "Teardown", mock.Anything, mock.Anything)
	mockBq.AssertNotCalled(t, "Teardown", mock.Anything, mock.Anything)
	mockFs.AssertNotCalled(t, "Teardown", mock.Anything, mock.Anything)
}
