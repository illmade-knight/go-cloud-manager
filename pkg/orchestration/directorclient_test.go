//go:build integration

package orchestration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// mockServiceDirector is a lightweight simulator of the remote ServiceDirector.
// It listens on the command topic and publishes a completion event.
type mockServiceDirector struct {
	t               *testing.T
	psClient        *pubsub.Client
	commandSub      *pubsub.Subscription
	completionTopic *pubsub.Topic
}

func (m *mockServiceDirector) listenAndReply(ctx context.Context) {
	err := m.commandSub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		var cmd orchestration.Command
		require.NoError(m.t, json.Unmarshal(msg.Data, &cmd))

		var dataflowName string
		var waiterKey string

		switch cmd.Instruction {
		case orchestration.Setup:
			var payload orchestration.FoundationalSetupPayload
			require.NoError(m.t, json.Unmarshal(cmd.Payload, &payload))
			dataflowName = payload.DataflowName
			waiterKey = dataflowName
		case orchestration.SetupDependent:
			var payload orchestration.DependentSetupPayload
			require.NoError(m.t, json.Unmarshal(cmd.Payload, &payload))
			dataflowName = payload.DataflowName
			waiterKey = dataflowName + "-dependent"
		}

		// Publish a success event.
		event := orchestration.CompletionEvent{
			Status: "dataflow_complete",
			Value:  waiterKey, // Respond with the key the client is waiting on.
		}
		eventData, err := json.Marshal(event)
		require.NoError(m.t, err)
		m.completionTopic.Publish(ctx, &pubsub.Message{Data: eventData})
	})
	if err != nil && err != context.Canceled {
		m.t.Errorf("mockServiceDirector listener failed: %v", err)
	}
}

func TestRemoteDirectorClient_Integration(t *testing.T) {
	t.Parallel()
	logger := zerolog.Nop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	projectID := "rdc-it-project"
	commandTopicID := "rdc-commands"
	completionTopicID := "rdc-events"
	commandSubID := "rdc-sub" // Subscription for the mock director

	// --- Arrange ---
	// 1. Setup Pub/Sub emulator
	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	// 2. Create topics for communication
	cmdTopic, err := psClient.CreateTopic(ctx, commandTopicID)
	require.NoError(t, err)
	compTopic, err := psClient.CreateTopic(ctx, completionTopicID)
	require.NoError(t, err)

	// 3. Create a subscription for the mock director to listen on
	cmdSub, err := psClient.CreateSubscription(ctx, commandSubID, pubsub.SubscriptionConfig{Topic: cmdTopic})
	require.NoError(t, err)

	// 4. Create the architecture object needed by the client
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		ServiceManagerSpec: servicemanager.ServiceManagerSpec{
			ServiceSpec: servicemanager.ServiceSpec{
				Deployment: &servicemanager.DeploymentSpec{
					EnvironmentVars: map[string]string{
						"SD_COMMAND_TOPIC":    commandTopicID,
						"SD_COMPLETION_TOPIC": completionTopicID,
					},
				},
			},
		},
	}

	// 5. Start the mock ServiceDirector listener
	mockDirector := &mockServiceDirector{
		t:               t,
		psClient:        psClient,
		commandSub:      cmdSub,
		completionTopic: compTopic,
	}
	go mockDirector.listenAndReply(ctx)

	// --- Act ---
	// Create the client we are testing. It will create its own listener subscription.
	client, err := orchestration.NewRemoteDirectorClient(ctx, arch, logger, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Teardown(context.Background()) })

	// --- Assert ---
	t.Run("sends foundational setup command and receives completion", func(t *testing.T) {
		event, err := client.TriggerFoundationalSetup(ctx, "dataflow-a")
		require.NoError(t, err)
		require.Equal(t, "dataflow_complete", string(event.Status))
		require.Equal(t, "dataflow-a", event.Value)
	})

	t.Run("sends dependent setup command and receives completion", func(t *testing.T) {
		urls := map[string]string{"service-a1": "https://service-a1.url"}
		event, err := client.TriggerDependentSetup(ctx, "dataflow-a", urls)
		require.NoError(t, err)
		require.Equal(t, "dataflow_complete", string(event.Status))
		require.Equal(t, "dataflow-a-dependent", event.Value)
	})
}
