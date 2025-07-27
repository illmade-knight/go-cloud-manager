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
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// TestOrchestratorCommandFlow verifies the full async command-and-reply loop using emulators.
func TestOrchestratorCommandFlow(t *testing.T) {
	logger := log.With().Str("test", "TestOrchestratorCommandFlow").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// --- 1. Arrange: Setup Emulators and Config ---
	projectID := "test-harness-project"
	commandTopicID := "director-commands-harness"
	commandSubID := "director-command-sub-harness"
	completionTopicID := "director-events-harness"
	dataflowTopicToCreate := "e2e-dataflow-topic"

	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() {
		psClient.Close()
	})

	// --- 2. Define Architecture and Create Orchestrator ---
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "in-memory-director", // A name for the service spec
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
	}

	// For this test, the ServiceManager just needs a messaging client.
	messagingClient := servicemanager.MessagingClientFromPubsubClient(psClient)
	sm, err := servicemanager.NewServiceManagerFromClients(messagingClient, nil, nil, arch.Environment, nil, logger)
	require.NoError(t, err)

	directorCfg.Commands = &servicedirector.PubsubConfig{
		CommandTopicID:    commandTopicID,
		CommandSubID:      commandSubID,
		CompletionTopicID: completionTopicID,
	}

	director, err := servicedirector.NewDirectServiceDirector(ctx, directorCfg, arch, sm, psClient, logger)
	require.NoError(t, err)
	require.NoError(t, director.Start())
	t.Cleanup(director.Shutdown)

	// --- 4. Act and Assert in Sequence ---

	// Step 4a: Await the "ready" signal from the ServiceDirector. This is the key pattern.
	err = orch.AwaitServiceReady(ctx, arch.ServiceManagerSpec.Name)
	require.NoError(t, err, "Did not receive 'service_ready' event from in-memory director")
	t.Log("Orchestrator confirmed ServiceDirector is ready.")

	// Step 4b: Now that the director is ready, trigger the dataflow setup.
	err = orch.TriggerDataflowSetup(ctx, "test-flow")
	require.NoError(t, err)

	// Step 4c: Await the "completion" signal for the dataflow setup.
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
