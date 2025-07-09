package servicemanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// setupServiceManagerTest creates a ServiceManager with mock sub-managers for testing.
func setupServiceManagerTest(t *testing.T) (*servicemanager.ServiceManager, *MockMessagingManager, *MockStorageManager, *MockBigQueryManager, *servicemanager.MicroserviceArchitecture) {
	mockMsg := new(MockMessagingManager)
	mockStore := new(MockStorageManager)
	mockBq := new(MockBigQueryManager)

	// Create a sample architecture with two dataflows
	arch := &servicemanager.MicroserviceArchitecture{
		Dataflows: map[string]servicemanager.ResourceGroup{
			"dataflow1": {
				Name: "dataflow1",
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: "df1-topic"}}},
				},
				Lifecycle: &servicemanager.LifecyclePolicy{Strategy: servicemanager.LifecycleStrategyEphemeral},
			},
			"dataflow2": {
				Name: "dataflow2",
				Resources: servicemanager.CloudResourcesSpec{
					GCSBuckets: []servicemanager.GCSBucket{{CloudResource: servicemanager.CloudResource{Name: "df2-bucket"}}},
				},
				Lifecycle: &servicemanager.LifecyclePolicy{Strategy: servicemanager.LifecycleStrategyPermanent}, // Not ephemeral
			},
		},
	}

	manager, err := servicemanager.NewServiceManagerFromManagers(mockMsg, mockStore, mockBq, zerolog.Nop())
	require.NoError(t, err)
	require.NotNil(t, manager)

	return manager, mockMsg, mockStore, mockBq, arch
}

// --- Tests ---

func TestServiceManager_SynthesizeAllResources(t *testing.T) {
	manager, _, _, _, arch := setupServiceManagerTest(t)

	// Action
	synthesized := manager.SynthesizeAllResources(arch)

	// Assert
	assert.Len(t, synthesized.Topics, 1, "Should have aggregated topics from all dataflows")
	assert.Len(t, synthesized.GCSBuckets, 1, "Should have aggregated buckets from all dataflows")
	assert.Equal(t, "df1-topic", synthesized.Topics[0].Name)
	assert.Equal(t, "df2-bucket", synthesized.GCSBuckets[0].Name)
}

func TestServiceManager_SetupAll(t *testing.T) {
	manager, mockMsg, mockStore, mockBq, arch := setupServiceManagerTest(t)
	ctx := context.Background()

	// Mock the sub-manager calls. Each should be called twice (once for each dataflow).
	mockMsg.On("CreateResources", ctx, mock.Anything).Return([]servicemanager.ProvisionedTopic{}, []servicemanager.ProvisionedSubscription{}, nil).Twice()
	mockStore.On("CreateResources", ctx, mock.Anything).Return([]servicemanager.ProvisionedGCSBucket{}, nil).Twice()
	mockBq.On("CreateResources", ctx, mock.Anything).Return([]servicemanager.ProvisionedBigQueryTable{}, []servicemanager.ProvisionedBigQueryDataset{}, nil).Twice()

	// Action
	_, err := manager.SetupAll(ctx, arch)

	// Assert
	assert.NoError(t, err)
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
}

func TestServiceManager_TeardownAll(t *testing.T) {
	manager, mockMsg, mockStore, mockBq, arch := setupServiceManagerTest(t)
	ctx := context.Background()

	// Mock the sub-manager calls. They should only be called ONCE, for the ephemeral dataflow ("dataflow1").
	df1Resources := arch.Dataflows["dataflow1"].Resources
	mockMsg.On("Teardown", ctx, df1Resources).Return(nil).Once()
	mockStore.On("Teardown", ctx, df1Resources).Return(nil).Once()
	mockBq.On("Teardown", ctx, df1Resources).Return(nil).Once()

	// Action
	err := manager.TeardownAll(ctx, arch)

	// Assert
	assert.NoError(t, err)
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
}

func TestServiceManager_SetupDataflow_Failure(t *testing.T) {
	manager, mockMsg, mockStore, mockBq, arch := setupServiceManagerTest(t)
	ctx := context.Background()
	storageErr := errors.New("bucket already exists and is owned by another project")

	// Mock two successes and one failure for the "dataflow1" call
	df1Resources := arch.Dataflows["dataflow1"].Resources
	mockMsg.On("CreateResources", ctx, df1Resources).Return([]servicemanager.ProvisionedTopic{}, []servicemanager.ProvisionedSubscription{}, nil).Once()
	mockStore.On("CreateResources", ctx, df1Resources).Return([]servicemanager.ProvisionedGCSBucket{}, storageErr).Once()
	mockBq.On("CreateResources", ctx, df1Resources).Return([]servicemanager.ProvisionedBigQueryTable{}, []servicemanager.ProvisionedBigQueryDataset{}, nil).Once()

	// Action
	_, err := manager.SetupDataflow(ctx, arch, "dataflow1")

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), storageErr.Error())
	// Verify all were called, even with the error, due to concurrency
	mockMsg.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockBq.AssertExpectations(t)
}

func TestServiceManager_TeardownDataflow_Skipped(t *testing.T) {
	manager, mockMsg, mockStore, mockBq, arch := setupServiceManagerTest(t)
	ctx := context.Background()

	// Action: Try to tear down "dataflow2", which is permanent
	err := manager.TeardownDataflow(ctx, arch, "dataflow2")

	// Assert
	assert.NoError(t, err, "Should not return an error when skipping a non-ephemeral teardown")
	// Assert that NO teardown methods were called
	mockMsg.AssertNotCalled(t, "Teardown", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "Teardown", mock.Anything, mock.Anything)
	mockBq.AssertNotCalled(t, "Teardown", mock.Anything, mock.Anything)
}
