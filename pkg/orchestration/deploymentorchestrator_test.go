package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Deployer ---

type mockDeployer struct {
	mu            sync.Mutex
	buildCalls    map[string]int
	deployCalls   map[string]int
	buildErr      error
	deployErr     error
	buildDelay    time.Duration
	deployDelay   time.Duration
	deployedURL   string
	teardownCalls map[string]int
}

// REFACTOR: The mutex is now used correctly to only protect the map write,
// allowing the time.Sleep to execute concurrently in different goroutines.
func (m *mockDeployer) Build(ctx context.Context, serviceName string, spec servicemanager.DeploymentSpec) (string, error) {
	if m.buildDelay > 0 {
		time.Sleep(m.buildDelay)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buildCalls == nil {
		m.buildCalls = make(map[string]int)
	}
	m.buildCalls[serviceName]++
	return fmt.Sprintf("gcr.io/project/%s:123", serviceName), m.buildErr
}

// REFACTOR: The mutex is now used correctly here as well.
func (m *mockDeployer) DeployService(ctx context.Context, serviceName, saEmail string, spec servicemanager.DeploymentSpec) (string, error) {
	if m.deployDelay > 0 {
		time.Sleep(m.deployDelay)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deployCalls == nil {
		m.deployCalls = make(map[string]int)
	}
	m.deployCalls[serviceName]++
	return m.deployedURL, m.deployErr
}

func (m *mockDeployer) BuildAndDeploy(ctx context.Context, serviceName, serviceAccountEmail string, spec servicemanager.DeploymentSpec) (string, error) {
	_, err := m.Build(ctx, serviceName, spec)
	if err != nil {
		return "", err
	}
	return m.DeployService(ctx, serviceName, serviceAccountEmail, spec)
}

func (m *mockDeployer) Teardown(ctx context.Context, serviceName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.teardownCalls == nil {
		m.teardownCalls = make(map[string]int)
	}
	m.teardownCalls[serviceName]++
	return nil
}

// --- Tests ---

func TestDeploymentManager_BuildAllServices(t *testing.T) {
	t.Parallel()
	logger := zerolog.Nop()
	ctx := context.Background()

	arch := &servicemanager.MicroserviceArchitecture{
		Dataflows: map[string]servicemanager.ResourceGroup{
			"flow-a": {
				Services: map[string]servicemanager.ServiceSpec{
					"service-a1": {Deployment: &servicemanager.DeploymentSpec{}},
					"service-a2": {Deployment: &servicemanager.DeploymentSpec{}},
				},
			},
			"flow-b": {
				Services: map[string]servicemanager.ServiceSpec{
					"service-b1": {Deployment: &servicemanager.DeploymentSpec{}},
				},
			},
		},
	}

	t.Run("successfully builds all services in parallel", func(t *testing.T) {
		t.Parallel()
		mock := &mockDeployer{buildDelay: 100 * time.Millisecond}
		manager := orchestration.NewDeploymentManager(arch, mock, logger)

		start := time.Now()
		images, err := manager.BuildAllServices(ctx)
		duration := time.Since(start)

		require.NoError(t, err)
		require.Len(t, images, 3)
		assert.Equal(t, 1, mock.buildCalls["service-a1"])
		assert.Equal(t, 1, mock.buildCalls["service-a2"])
		assert.Equal(t, 1, mock.buildCalls["service-b1"])

		// Sequential time would be >300ms. This check is now robust and should pass.
		assert.Less(t, duration, 250*time.Millisecond, "Total duration should be closer to one build time (~100ms), not three (~300ms)")
	})

	t.Run("returns aggregated error if any build fails", func(t *testing.T) {
		t.Parallel()
		mock := &mockDeployer{buildErr: errors.New("build failed")}
		manager := orchestration.NewDeploymentManager(arch, mock, logger)

		_, err := manager.BuildAllServices(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "build failed")
	})
}

func TestDeploymentManager_DeployApplicationServices(t *testing.T) {
	t.Parallel()
	logger := zerolog.Nop()
	ctx := context.Background()

	arch := &servicemanager.MicroserviceArchitecture{
		Dataflows: map[string]servicemanager.ResourceGroup{
			"flow-a": {
				Services: map[string]servicemanager.ServiceSpec{
					"service-a1": {Deployment: &servicemanager.DeploymentSpec{}},
					"service-a2": {Deployment: &servicemanager.DeploymentSpec{}},
				},
			},
		},
	}

	saEmails := map[string]string{"service-a1": "sa1@email", "service-a2": "sa2@email"}
	builtImages := map[string]string{"service-a1": "img1", "service-a2": "img2"}

	t.Run("successfully deploys all services in parallel", func(t *testing.T) {
		t.Parallel()
		mock := &mockDeployer{deployDelay: 100 * time.Millisecond, deployedURL: "https://service.url"}
		manager := orchestration.NewDeploymentManager(arch, mock, logger)

		start := time.Now()
		urls, err := manager.DeployApplicationServices(ctx, "flow-a", saEmails, builtImages, "http://director")
		duration := time.Since(start)

		require.NoError(t, err)
		require.Len(t, urls, 2)
		assert.Equal(t, 1, mock.deployCalls["service-a1"])
		assert.Equal(t, 1, mock.deployCalls["service-a2"])

		// Sequential time would be >200ms. This check is robust and should pass.
		assert.Less(t, duration, 175*time.Millisecond, "Total duration should be closer to one deploy time (~100ms), not two (~200ms)")
	})
}
