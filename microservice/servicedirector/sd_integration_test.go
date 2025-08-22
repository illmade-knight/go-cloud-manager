//go:build integration

package servicedirector_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/microservice/servicedirector"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-dataflow/pkg/microservice"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- Mocks for Dependencies ---

// MockServiceManager allows us to assert that the Director calls the correct methods.
// It fully implements the servicemanager.IServiceManager interface.
type MockServiceManager struct {
	mock.Mock
}

func (m *MockServiceManager) SetupFoundationalDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) (*servicemanager.ProvisionedResources, error) {
	args := m.Called(ctx, arch, dataflowName)
	return args.Get(0).(*servicemanager.ProvisionedResources), args.Error(1)
}

func (m *MockServiceManager) SetupDependentDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) error {
	args := m.Called(ctx, arch, dataflowName)
	return args.Error(0)
}

func (m *MockServiceManager) TeardownDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) error {
	args := m.Called(ctx, arch, dataflowName)
	return args.Error(0)
}

func (m *MockServiceManager) TeardownAll(ctx context.Context, arch *servicemanager.MicroserviceArchitecture) error {
	args := m.Called(ctx, arch)
	return args.Error(0)
}

func (m *MockServiceManager) VerifyDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) error {
	args := m.Called(ctx, arch, dataflowName)
	return args.Error(0)
}

func (m *MockServiceManager) SetupAll(ctx context.Context, arch *servicemanager.MicroserviceArchitecture) (*servicemanager.ProvisionedResources, error) {
	args := m.Called(ctx, arch)
	return args.Get(0).(*servicemanager.ProvisionedResources), args.Error(1)
}

// MockIAMManager allows us to assert that the Director calls the correct methods.
type MockIAMManager struct {
	mock.Mock
}

func (m *MockIAMManager) ApplyIAMForDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) (map[string]iam.PolicyBinding, error) {
	args := m.Called(ctx, arch, dataflowName)
	return args.Get(0).(map[string]iam.PolicyBinding), args.Error(1)
}

// --- Test ---

func TestServiceDirector_Integration(t *testing.T) {
	// --- Arrange ---
	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "sd-it-project"
	commandTopicID := "sd-it-commands"
	commandSubID := "sd-it-sub"
	completionTopicID := "sd-it-events"
	dataflowToTest := "test-flow"

	// 1. Setup Pub/Sub emulator and topics
	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	_, err = psClient.CreateTopic(ctx, commandTopicID)
	require.NoError(t, err)
	_, err = psClient.CreateTopic(ctx, completionTopicID)
	require.NoError(t, err)

	// 2. Create a minimal architecture object for the Director to use.
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		Dataflows:   map[string]servicemanager.ResourceGroup{dataflowToTest: {}},
	}

	// 3. Create the Director's configuration.
	directorCfg := &servicedirector.Config{
		BaseConfig: microservice.BaseConfig{ProjectID: projectID},
		Commands: &servicedirector.PubsubConfig{
			CommandTopicID:    commandTopicID,
			CommandSubID:      commandSubID,
			CompletionTopicID: completionTopicID,
			Options:           pubsubConn.ClientOptions,
		},
	}

	// 4. Setup mock managers and their expected calls.
	mockServiceManager := new(MockServiceManager)
	mockIAMManager := new(MockIAMManager)

	mockServiceManager.On("SetupFoundationalDataflow", mock.Anything, mock.AnythingOfType("*servicemanager.MicroserviceArchitecture"), dataflowToTest).Return(&servicemanager.ProvisionedResources{}, nil)
	mockIAMManager.On("ApplyIAMForDataflow", mock.Anything, mock.AnythingOfType("*servicemanager.MicroserviceArchitecture"), dataflowToTest).Return(make(map[string]iam.PolicyBinding), nil)

	// 5. Create and start the Director instance with the mocked dependencies.
	director, err := servicedirector.NewDirectServiceDirector(ctx, directorCfg, arch, mockServiceManager, mockIAMManager, nil, psClient, logger)
	require.NoError(t, err)

	go func() {
		if err := director.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("director.Start() returned an unexpected error: %v", err)
		}
	}()
	t.Cleanup(func() { director.Shutdown(context.Background()) })
	<-director.Ready() // Wait for the service to be fully initialized.

	// --- Act ---
	t.Log("Publishing 'setup' command to ServiceDirector...")
	payload := orchestration.FoundationalSetupPayload{DataflowName: dataflowToTest}
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)

	cmd := orchestration.Command{
		Instruction: orchestration.Setup,
		Payload:     payloadBytes,
	}
	cmdData, err := json.Marshal(cmd)
	require.NoError(t, err)

	// Create a listener to wait for the completion event.
	completionSubID := "test-completion-listener"
	completionSub, err := psClient.CreateSubscription(ctx, completionSubID, pubsub.SubscriptionConfig{Topic: psClient.Topic(completionTopicID)})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		_ = completionSub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
			msg.Ack()
			var event orchestration.CompletionEvent
			_ = json.Unmarshal(msg.Data, &event)
			if event.Value == dataflowToTest && event.Status == orchestration.DataflowComplete {
				close(done)
			}
		})
	}()

	psClient.Topic(commandTopicID).Publish(ctx, &pubsub.Message{Data: cmdData})

	select {
	case <-done:
		t.Log("Received completion event from ServiceDirector.")
	case <-ctx.Done():
		t.Fatal("Test timed out waiting for completion event from ServiceDirector")
	}

	// --- Assert ---
	t.Log("Verifying that the correct manager methods were called...")
	mockServiceManager.AssertExpectations(t)
	mockIAMManager.AssertExpectations(t)
	t.Log("✅ Manager calls verified.")
}
