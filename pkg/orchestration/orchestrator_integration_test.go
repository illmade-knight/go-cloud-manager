//go:build integration

package orchestration_test

import (
	"context"
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

// TestOrchestratorCommandFlow verifies the full async command-and-reply loop using emulators.
func TestOrchestratorCommandFlow(t *testing.T) {
	logger := log.With().Str("test", "TestOrchestratorCommandFlow").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// --- 1. Setup ---
	projectID := "test-harness-project"
	commandTopicID := "director-commands-harness"
	commandSubID := "director-command-sub-harness"
	completionTopicID := "director-events-harness"
	dataflowTopicToCreate := "e2e-dataflow-topic"

	// Start the emulator and create a client for it.
	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	defer psClient.Close()

	// --- 2. Create the Orchestrator ---
	// The test now defines a MicroserviceArchitecture as the main configuration.
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			ProjectID: projectID,
			Region:    "us-central1", // Required by the deployer
		},
		// Define the ServiceManagerSpec with the command topics needed by the orchestrator.
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Deployment: &servicemanager.DeploymentSpec{
				EnvironmentVars: map[string]string{
					"SD_COMMAND_TOPIC":    commandTopicID,
					"SD_COMPLETION_TOPIC": completionTopicID,
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

	// Create the orchestrator using the new architecture-driven constructor.
	// We pass 'nil' for the schema registry as it's not needed for this flow.
	orch, err := orchestration.NewOrchestratorWithClients(ctx, arch, psClient, logger)
	require.NoError(t, err)
	t.Cleanup(orch.Close)

	// Wait for the orchestrator to finish its initialization (creating topics).
	t.Log("Waiting for orchestrator to become ready...")
	require.NoError(t, orchestration.WaitForState(ctx, orch, orchestration.StateCommandInfraReady), "Orchestrator failed to initialize command infrastructure")
	t.Log("Orchestrator is ready.")

	// --- 3. Run ServiceDirector In-Memory ---
	t.Log("Starting in-memory ServiceDirector...")
	directorCfg := &servicedirector.Config{
		BaseConfig:          microservice.BaseConfig{ProjectID: projectID},
		CommandTopic:        commandTopicID,
		CommandSubscription: commandSubID,
		CompletionTopic:     completionTopicID,
	}
	loader := orchestration.NewEmbeddedArchitectureLoader(arch)
	messagingClient := servicemanager.MessagingClientFromPubsubClient(psClient)
	sm, err := servicemanager.NewServiceManagerFromClients(messagingClient, nil, nil, arch.Environment, nil, nil, logger)
	require.NoError(t, err)
	director, err := servicedirector.NewDirectServiceDirector(ctx, directorCfg, loader, sm, psClient, logger)
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
