//go:build integration

package servicemanager_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMessagingManager_Integration tests the manager against a live Pub/Sub emulator.
func TestMessagingManager_Integration(t *testing.T) {
	// --- ARRANGE: Set up context, configuration, and clients ---
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "msg-it-project"
	runID := uuid.New().String()[:8]
	topicName := fmt.Sprintf("it-topic-%s", runID)
	subName := fmt.Sprintf("it-sub-%s", runID)

	resources := servicemanager.CloudResourcesSpec{
		Topics: []servicemanager.TopicConfig{
			{CloudResource: servicemanager.CloudResource{Name: topicName}},
		},
		Subscriptions: []servicemanager.SubscriptionConfig{
			{
				CloudResource: servicemanager.CloudResource{Name: subName},
				Topic:         topicName,
			},
		},
	}

	// Set up the Pub/Sub emulator and a client connected to it.
	t.Log("Setting up Pub/Sub emulator...")
	// REFACTOR: Use the updated GetDefaultPubsubConfig.
	psConnection := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID))

	// Create the MessagingManager instance using the v2 adapter.
	logger := zerolog.New(zerolog.NewConsoleWriter())
	environment := servicemanager.Environment{ProjectID: projectID}
	// REFACTOR: Use the new factory to create the v2-compliant messaging client.
	messagingAdapter, err := servicemanager.CreateGoogleMessagingClient(ctx, projectID, psConnection.ClientOptions...)
	require.NoError(t, err)
	manager, err := servicemanager.NewMessagingManager(messagingAdapter, logger, environment)
	require.NoError(t, err)

	// --- ACT & ASSERT: Execute and verify each lifecycle phase ---

	// Phase 1: CREATE Resources
	t.Run("CreateResources", func(t *testing.T) {
		t.Log("--- Starting CreateResources ---")
		provTopics, provSubs, createErr := manager.CreateResources(ctx, resources)
		require.NoError(t, createErr)
		assert.Len(t, provTopics, 1, "Should provision one topic")
		assert.Len(t, provSubs, 1, "Should provision one subscription")
		t.Log("--- CreateResources finished successfully ---")
	})

	// Phase 2: VERIFY Resources Exist
	t.Run("VerifyResourcesExist", func(t *testing.T) {
		t.Log("--- Verifying resources exist in emulator ---")
		// REFACTOR: Verification must now use a raw v2 client to check the emulator state.
		rawPSClient, err := pubsub.NewClient(ctx, projectID, psConnection.ClientOptions...)
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = rawPSClient.Close()
		})

		topicAdmin := rawPSClient.TopicAdminClient
		_, err = topicAdmin.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: fmt.Sprintf("projects/%s/topics/%s", projectID, topicName)})
		require.NoError(t, err, "Topic should exist in the emulator")

		subAdmin := rawPSClient.SubscriptionAdminClient
		_, err = subAdmin.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subName)})
		require.NoError(t, err, "Subscription should exist in the emulator")

		t.Log("--- Verification successful, all resources exist ---")
	})

	// Phase 3: TEARDOWN Resources
	t.Run("TeardownResources", func(t *testing.T) {
		t.Log("--- Starting Teardown ---")
		teardownErr := manager.Teardown(ctx, resources)
		require.NoError(t, teardownErr, "Teardown should not fail")
		t.Log("--- Teardown finished successfully ---")
	})

	// Phase 4: VERIFY Resources are Deleted
	t.Run("VerifyResourcesDeleted", func(t *testing.T) {
		t.Log("--- Verifying resources are deleted from emulator ---")
		rawPSClient, err := pubsub.NewClient(ctx, projectID, psConnection.ClientOptions...)
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = rawPSClient.Close()
		})

		topicAdmin := rawPSClient.TopicAdminClient
		_, err = topicAdmin.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: fmt.Sprintf("projects/%s/topics/%s", projectID, topicName)})
		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err), "Topic should NOT exist after teardown")

		subAdmin := rawPSClient.SubscriptionAdminClient
		_, err = subAdmin.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subName)})
		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err), "Subscription should NOT exist after teardown")

		t.Log("--- Deletion verification successful ---")
	})
}
