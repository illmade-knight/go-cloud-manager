//go:build integration

package servicemanager_test

import (
	"cloud.google.com/go/pubsub"
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
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
	psConnection := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, psConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() {
		closeErr := psClient.Close()
		if closeErr != nil {
			t.Logf("Error closing Pub/Sub client: %v", closeErr)
		}
	})

	// Create the MessagingManager instance.
	logger := zerolog.New(zerolog.NewConsoleWriter())
	environment := servicemanager.Environment{ProjectID: projectID}
	messagingAdapter := servicemanager.MessagingClientFromPubsubClient(psClient)
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
		topic := psClient.Topic(topicName)
		exists, existsErr := topic.Exists(ctx)
		require.NoError(t, existsErr)
		assert.True(t, exists, "Pub/Sub topic should exist after creation")

		sub := psClient.Subscription(subName)
		exists, existsErr = sub.Exists(ctx)
		require.NoError(t, existsErr)
		assert.True(t, exists, "Pub/Sub subscription should exist after creation")
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
		topicExists, err := psClient.Topic(topicName).Exists(ctx)
		require.NoError(t, err)
		assert.False(t, topicExists, "Pub/Sub topic should NOT exist after teardown")

		subExists, err := psClient.Subscription(subName).Exists(ctx)
		require.NoError(t, err)
		assert.False(t, subExists, "Pub/Sub subscription should NOT exist after teardown")
		t.Log("--- Deletion verification successful ---")
	})
}
