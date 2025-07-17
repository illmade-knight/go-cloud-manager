//go:build integration

package orchestration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/microservice"
	"github.com/illmade-knight/go-cloud-manager/microservice/servicedirector"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-iot/helpers/emulators"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// EmbeddedArchitectureLoader is a mock loader for the test.
type EmbeddedArchitectureLoader struct {
	arch *servicemanager.MicroserviceArchitecture
}

func (l *EmbeddedArchitectureLoader) LoadArchitecture(ctx context.Context) (*servicemanager.MicroserviceArchitecture, error) {
	return l.arch, nil
}

func (l *EmbeddedArchitectureLoader) LoadResourceGroup(ctx context.Context, groupName string) (*servicemanager.ResourceGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (l *EmbeddedArchitectureLoader) WriteProvisionedResources(ctx context.Context, resources *servicemanager.ProvisionedResources) error {
	return fmt.Errorf("not implemented")
}

// TestOrchestratorCommandFlow verifies the full async command-and-reply loop using emulators.
func TestOrchestratorCommandFlow(t *testing.T) {
	logger := log.With().Str("test", "TestOrchestratorCommandFlow").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// --- 1. Setup Emulated Pub/Sub ---
	t.Log("Setting up Pub/Sub emulator...")
	projectID := "test-harness-project"
	commandTopicID := "director-commands-harness"
	completionTopicID := "director-events-harness"
	dataflowTopicToCreate := "e2e-dataflow-topic"

	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))

	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	defer psClient.Close()

	// --- 2. Create the Orchestrator ---
	t.Log("Creating orchestrator...")
	orchCfg := orchestration.Config{
		ProjectID:       projectID,
		Region:          "us-central1",
		CommandTopic:    commandTopicID,
		CompletionTopic: completionTopicID,
	}
	orch, err := orchestration.NewOrchestratorFromClients(ctx, orchCfg, logger, psClient)
	require.NoError(t, err)
	t.Cleanup(orch.Close)

	t.Log("Waiting for orchestrator to create command topics...")
	require.NoError(t, waitForState(ctx, orch, orchestration.StateCommandInfraReady), "Orchestrator failed to initialize command infrastructure")
	t.Log("Orchestrator is ready.")

	// --- 3. Run ServiceDirector In-Memory ---
	t.Log("Starting in-memory ServiceDirector...")
	directorCfg := &servicedirector.Config{
		BaseConfig:          microservice.BaseConfig{ProjectID: projectID},
		CommandTopic:        commandTopicID,
		CommandSubscription: "director-command-sub-harness",
		CompletionTopic:     completionTopicID,
	}
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
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
	loader := orchestration.NewEmbeddedArchitectureLoader(arch)

	// Create the ServiceManager with the emulated client.
	messagingClient := servicemanager.MessagingClientFromPubsubClient(psClient)
	sm, err := servicemanager.NewServiceManagerFromClients(messagingClient, nil, nil, arch.Environment, nil, nil, logger)
	require.NoError(t, err)

	// Use the single, correct constructor for the director.
	director, err := servicedirector.NewServiceDirector(ctx, directorCfg, loader, sm, psClient, logger)
	require.NoError(t, err)
	require.NoError(t, director.Start())
	t.Cleanup(director.Shutdown)

	// --- 4. Act and Assert ---
	err = orch.TriggerDataflowSetup(ctx, "test-flow")
	require.NoError(t, err)

	err = orch.AwaitDataflowReady(ctx, "test-flow")
	require.NoError(t, err, "Did not receive completion event in time")

	// --- 5. Final Verification ---
	t.Log("Verifying that the dataflow resource was created in the emulator...")
	topic := psClient.Topic(dataflowTopicToCreate)
	exists, err := topic.Exists(ctx)
	require.NoError(t, err, "Failed to check for new topic existence")
	require.True(t, exists, "The ServiceDirector should have created the dataflow topic in the emulator")

	t.Log("✅ Resource creation verified. Test successful.")
}

// waitForState is a test helper to listen on the state channel for a target state.
func waitForState(ctx context.Context, orch *orchestration.Orchestrator, targetState orchestration.OrchestratorState) error {
	for {
		select {
		case state, ok := <-orch.StateChan():
			if !ok {
				return fmt.Errorf("orchestrator state channel closed prematurely")
			}
			log.Info().Str("state", string(state)).Msg("Orchestrator state changed")
			if state == targetState {
				return nil // Success!
			}
			if state == orchestration.StateError {
				return fmt.Errorf("orchestrator entered an error state")
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
			return fmt.Errorf("timed out waiting for orchestrator state: %s", targetState)
		}
	}
}
