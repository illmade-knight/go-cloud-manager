//go:build integration

package orchestration_test

import (
	"context"
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
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// REFACTOR: Add a local mock IAM client to satisfy the new constructor signature.
// This allows the test to remain focused on the command/reply loop.
type mockIAMClient struct{}

func (m *mockIAMClient) EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error) {
	return "mock-" + accountName + "@project.iam.gserviceaccount.com", nil
}
func (m *mockIAMClient) AddResourceIAMBinding(ctx context.Context, binding iam.IAMBinding, member string) error {
	return nil
}
func (m *mockIAMClient) RemoveResourceIAMBinding(ctx context.Context, binding iam.IAMBinding, member string) error {
	return nil
}
func (m *mockIAMClient) CheckResourceIAMBinding(ctx context.Context, binding iam.IAMBinding, member string) (bool, error) {
	return true, nil
}
func (m *mockIAMClient) AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error {
	return nil
}
func (m *mockIAMClient) DeleteServiceAccount(ctx context.Context, accountName string) error {
	return nil
}
func (m *mockIAMClient) AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error {
	return nil
}
func (m *mockIAMClient) Close() error { return nil }

// TestOrchestratorCommandFlow verifies the full asynchronous command-and-reply loop
// between the Orchestrator and an in-memory ServiceDirector using Pub/Sub emulators.
// This test ensures that the core event-driven communication mechanism is working correctly.
func TestOrchestratorCommandFlow(t *testing.T) {
	// --- Arrange: Setup Emulators and Config ---
	logger := log.With().Str("test", "TestOrchestratorCommandFlow").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "test-harness-project"
	commandTopicID := "director-commands-harness"
	commandSubID := "director-command-sub-harness"
	completionTopicID := "director-events-harness"
	dataflowTopicToCreate := "e2e-dataflow-topic"

	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = psClient.Close()
	})

	// --- 2. Define Architecture and Create Orchestrator ---
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "in-memory-director",
			Deployment: &servicemanager.DeploymentSpec{
				EnvironmentVars: map[string]string{
					"SD_COMMAND_TOPIC":        commandTopicID,
					"SD_COMPLETION_TOPIC":     completionTopicID,
					"SD_COMMAND_SUBSCRIPTION": commandSubID,
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: dataflowTopicToCreate}},
					},
				},
			},
		},
	}

	// Use NewOrchestratorWithClients to inject the emulator client and a nil deployer.
	orch, err := orchestration.NewOrchestratorWithClients(ctx, arch, psClient, nil, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = orch.Teardown(context.Background()) })

	// Wait for the orchestrator to finish its initialization (creating topics).
	require.NoError(t, orchestration.WaitForState(ctx, orch, orchestration.StateCommandInfraReady), "Orchestrator failed to initialize command infrastructure")
	t.Log("Orchestrator is ready.")

	// --- 3. Run ServiceDirector In-Memory ---
	t.Log("Starting in-memory ServiceDirector...")
	directorCfg := &servicedirector.Config{
		BaseConfig: microservice.BaseConfig{ProjectID: projectID},
		Commands: &servicedirector.PubsubConfig{
			CommandTopicID:    commandTopicID,
			CommandSubID:      commandSubID,
			CompletionTopicID: completionTopicID,
			Options:           pubsubConn.ClientOptions,
		},
	}

	messagingClient := servicemanager.MessagingClientFromPubsubClient(psClient)
	sm, err := servicemanager.NewServiceManagerFromClients(messagingClient, nil, nil, arch.Environment, nil, logger)
	require.NoError(t, err)

	// REFACTOR: Create mock IAM dependencies and pass them to the updated constructor.
	mockIamClient := &mockIAMClient{}
	iamManager, err := iam.NewIAMManager(mockIamClient, logger)
	require.NoError(t, err)

	director, err := servicedirector.NewDirectServiceDirector(ctx, directorCfg, arch, sm, iamManager, mockIamClient, psClient, logger)
	require.NoError(t, err)

	go func() {
		if err := director.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("director.Start() returned an unexpected error: %v", err)
		}
	}()
	t.Cleanup(func() { director.Shutdown(context.Background()) })
	<-director.Ready()

	// --- 4. Act and Assert in Sequence ---
	t.Run("Act and Assert Event Flow", func(t *testing.T) {
		// Step 4a: Await the "ready" signal from the ServiceDirector.
		err = orch.AwaitServiceReady(ctx, arch.ServiceManagerSpec.Name)
		require.NoError(t, err, "Did not receive 'service_ready' event from in-memory director")
		t.Log("Orchestrator confirmed ServiceDirector is ready.")

		// Step 4b: Now that the director is ready, trigger the dataflow setup.
		err = orch.TriggerDataflowResourceCreation(ctx, "test-flow")
		require.NoError(t, err)

		// Step 4c: Await the "completion" signal for the dataflow setup.
		err = orch.AwaitDataflowReady(ctx, "test-flow")
		require.NoError(t, err, "Did not receive completion event in time")
	})

	// --- 5. Final Verification ---
	t.Run("Verify Resource Creation", func(t *testing.T) {
		t.Log("Verifying that the dataflow resource was created in the emulator...")
		topic := psClient.Topic(dataflowTopicToCreate)
		exists, err := topic.Exists(ctx)
		require.NoError(t, err, "Failed to check for new topic existence")
		require.True(t, exists, "The ServiceDirector should have created the dataflow topic in the emulator")
		t.Log("✅ Resource creation verified. Test successful.")
	})
}
