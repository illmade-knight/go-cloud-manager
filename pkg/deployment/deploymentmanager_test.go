package deployment_test

import (
	"context"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockIAMManager mocks the IAMManager for testing the DeploymentManager's orchestration.
type MockIAMManager struct {
	mock.Mock
}

func (m *MockIAMManager) ApplyIAMForService(ctx context.Context, dataflow servicemanager.ResourceGroup, serviceName string) error {
	args := m.Called(ctx, dataflow, serviceName)
	return args.Error(0)
}

func TestDeploymentManager_DeployIAMForDataflow(t *testing.T) {
	// --- Arrange ---
	mockIAM := new(MockIAMManager)
	deploymentManager := deployment.NewDeploymentManager(mockIAM, zerolog.Nop())

	// Create a test architecture with a dataflow containing two services.
	arch := &servicemanager.MicroserviceArchitecture{
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Name: "test-flow",
				Services: map[string]servicemanager.ServiceSpec{
					"service-A": {},
					"service-B": {},
				},
			},
		},
	}
	dataflow := arch.Dataflows["test-flow"]

	// Set expectations: ApplyIAMForService should be called once for each service.
	mockIAM.On("ApplyIAMForService", mock.Anything, dataflow, "service-A").Return(nil).Once()
	mockIAM.On("ApplyIAMForService", mock.Anything, dataflow, "service-B").Return(nil).Once()

	// --- Act ---
	err := deploymentManager.DeployIAMForDataflow(context.Background(), arch, "test-flow")

	// --- Assert ---
	require.NoError(t, err)
	mockIAM.AssertExpectations(t)
}
