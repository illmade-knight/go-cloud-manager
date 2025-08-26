//go:build integration

package orchestration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// mockServiceDirector is a lightweight simulator of the remote ServiceDirector.
// REFACTOR: Updated to use v2 pubsub objects.
type mockServiceDirector struct {
	t                 *testing.T
	commandSubscriber *pubsub.Subscriber
	completionTopic   *pubsub.Publisher
}

func (m *mockServiceDirector) listenAndReply(ctx context.Context) {
	err := m.commandSubscriber.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
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
		result := m.completionTopic.Publish(ctx, &pubsub.Message{Data: eventData})
		_, err = result.Get(ctx)
		require.NoError(m.t, err)
	})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.Canceled {
			// This is an expected error when the test context is canceled.
			return
		}
		if !errors.Is(err, context.Canceled) {
			m.t.Errorf("mockServiceDirector listener failed: %v", err)
		}
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
	// REFACTOR: Use the updated emulators package.
	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID))
	conn, err := grpc.NewClient(pubsubConn.HTTPEndpoint.Endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
	})
	opts := []option.ClientOption{option.WithGRPCConn(conn)}

	psClient, err := pubsub.NewClient(ctx, projectID, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	// 2. Create topics and a subscription for the mock director
	// REFACTOR: Use v2 admin clients to create resources.
	topicAdmin := psClient.TopicAdminClient
	subAdmin := psClient.SubscriptionAdminClient

	cmdTopicName := fmt.Sprintf("projects/%s/topics/%s", projectID, commandTopicID)
	_, err = topicAdmin.CreateTopic(ctx, &pubsubpb.Topic{Name: cmdTopicName})
	require.NoError(t, err)

	compTopicName := fmt.Sprintf("projects/%s/topics/%s", projectID, completionTopicID)
	_, err = topicAdmin.CreateTopic(ctx, &pubsubpb.Topic{Name: compTopicName})
	require.NoError(t, err)

	cmdSubName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, commandSubID)
	_, err = subAdmin.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  cmdSubName,
		Topic: cmdTopicName,
	})
	require.NoError(t, err)

	// 3. Create the architecture object needed by the client
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		ServiceManagerSpec: servicemanager.ServiceManagerSpec{
			CommandTopic:    servicemanager.ServiceMapping{Name: commandTopicID},
			CompletionTopic: servicemanager.ServiceMapping{Name: completionTopicID},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"director-infra": {
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: commandTopicID}},
						{CloudResource: servicemanager.CloudResource{Name: completionTopicID}},
					},
				},
			},
		},
	}

	// 4. Start the mock ServiceDirector listener
	mockDirector := &mockServiceDirector{
		t:                 t,
		commandSubscriber: psClient.Subscriber(commandSubID),
		completionTopic:   psClient.Publisher(completionTopicID),
	}
	go mockDirector.listenAndReply(ctx)

	// --- Act ---
	// Create the client we are testing. It will create its own listener subscription.
	client, err := orchestration.NewRemoteDirectorClient(ctx, arch, logger, opts...)
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
