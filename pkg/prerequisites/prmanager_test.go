package prerequisites_test

import (
	"context"
	"errors"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/prerequisites"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockServiceAPIClient is a mock implementation of the ServiceAPIClient interface.
type MockServiceAPIClient struct {
	mock.Mock
}

func (m *MockServiceAPIClient) GetEnabledServices(ctx context.Context, projectID string) (map[string]struct{}, error) {
	args := m.Called(ctx, projectID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]struct{}), args.Error(1)
}

func (m *MockServiceAPIClient) EnableServices(ctx context.Context, projectID string, services []string) error {
	args := m.Called(ctx, projectID, services)
	return args.Error(0)
}

func (m *MockServiceAPIClient) Close() error {
	args := m.Called()
	return args.Error(0)
}

// setupManagerTest is a helper to create a Manager with a mock client.
func setupManagerTest(t *testing.T) (*prerequisites.Manager, *MockServiceAPIClient) {
	mockClient := new(MockServiceAPIClient)
	logger := zerolog.Nop()
	manager := prerequisites.NewManager(mockClient, logger)
	require.NotNil(t, manager)
	return manager, mockClient
}

// simpleArch creates a basic architecture that requires a known set of APIs.
func simpleArch() *servicemanager.MicroserviceArchitecture {
	return &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			ProjectID: "test-project",
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"dataflow-1": {
				Resources: servicemanager.CloudResourcesSpec{
					Topics:     []servicemanager.TopicConfig{{}},
					GCSBuckets: []servicemanager.GCSBucket{{}},
				},
			},
		},
	}
}

func TestManager_CheckAndEnable_AllApisAlreadyEnabled(t *testing.T) {
	// ARRANGE
	manager, mockClient := setupManagerTest(t)
	arch := simpleArch() // Requires pubsub and storage
	ctx := context.Background()

	// Configure the mock to report that all required APIs are already enabled.
	enabledServices := map[string]struct{}{
		"pubsub.googleapis.com":  {},
		"storage.googleapis.com": {},
		"iam.googleapis.com":     {}, // An extra one to ensure it's not a problem
	}
	mockClient.On("GetEnabledServices", ctx, "test-project").Return(enabledServices, nil).Once()

	// ACT
	err := manager.CheckAndEnable(ctx, arch)

	// ASSERT
	require.NoError(t, err)
	// Crucially, assert that EnableServices was NOT called.
	mockClient.AssertNotCalled(t, "EnableServices", mock.Anything, mock.Anything, mock.Anything)
	mockClient.AssertExpectations(t)
}

func TestManager_CheckAndEnable_SomeApisMissing(t *testing.T) {
	// ARRANGE
	manager, mockClient := setupManagerTest(t)
	arch := simpleArch() // Requires pubsub and storage
	ctx := context.Background()

	// Configure the mock to report that only the storage API is enabled.
	enabledServices := map[string]struct{}{
		"storage.googleapis.com": {},
	}
	mockClient.On("GetEnabledServices", ctx, "test-project").Return(enabledServices, nil).Once()

	// Expect the manager to call EnableServices with the missing API.
	apisToEnable := []string{"pubsub.googleapis.com"}
	mockClient.On("EnableServices", ctx, "test-project", apisToEnable).Return(nil).Once()

	// ACT
	err := manager.CheckAndEnable(ctx, arch)

	// ASSERT
	require.NoError(t, err)
	// Assert that all mock expectations were met.
	mockClient.AssertExpectations(t)
}

func TestManager_CheckAndEnable_GetEnabledFails(t *testing.T) {
	// ARRANGE
	manager, mockClient := setupManagerTest(t)
	arch := simpleArch()
	ctx := context.Background()
	expectedErr := errors.New("permission denied")

	// Configure the mock to fail when checking for services.
	mockClient.On("GetEnabledServices", ctx, "test-project").Return(nil, expectedErr).Once()

	// ACT
	err := manager.CheckAndEnable(ctx, arch)

	// ASSERT
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
	mockClient.AssertNotCalled(t, "EnableServices", mock.Anything, mock.Anything, mock.Anything)
	mockClient.AssertExpectations(t)
}

func TestManager_CheckAndEnable_EnableFails(t *testing.T) {
	// ARRANGE
	manager, mockClient := setupManagerTest(t)
	arch := simpleArch()
	ctx := context.Background()
	expectedErr := errors.New("billing account not linked")

	// Configure mocks to find a missing API but then fail during the enable call.
	enabledServices := map[string]struct{}{"storage.googleapis.com": {}}
	mockClient.On("GetEnabledServices", ctx, "test-project").Return(enabledServices, nil).Once()
	apisToEnable := []string{"pubsub.googleapis.com"}
	mockClient.On("EnableServices", ctx, "test-project", apisToEnable).Return(expectedErr).Once()

	// ACT
	err := manager.CheckAndEnable(ctx, arch)

	// ASSERT
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
	mockClient.AssertExpectations(t)
}
