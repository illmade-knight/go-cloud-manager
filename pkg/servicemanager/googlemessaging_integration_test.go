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
)

// TestMessagingManager_Integration tests the manager against a live Pub/Sub emulator.
func TestMessagingManager_Integration(t *testing.T) {
	ctx := context.Background()
	projectID := "msg-it-project"
	runID := uuid.New().String()[:8]

	// --- 1. Define Resource Names and Configuration ---
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

	// --- 2. Setup Emulator and Real Pub/Sub Client ---
	t.Log("Setting up Pub/Sub emulator...")
	psConnection := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, psConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() {
		err = psClient.Close()
		if err != nil {
			t.Logf("Error closing Pub/Sub emulator: %v", err)
		}
	})

	// --- 3. Create the MessagingManager ---
	logger := zerolog.New(zerolog.NewConsoleWriter())
	environment := servicemanager.Environment{ProjectID: projectID}

	// Create a real Pub/Sub client adapter for the manager
	messagingAdapter := servicemanager.MessagingClientFromPubsubClient(psClient)

	manager, err := servicemanager.NewMessagingManager(messagingAdapter, logger, environment)
	require.NoError(t, err)

	// =========================================================================
	// --- Phase 1: CREATE Resources ---
	// =========================================================================
	t.Log("--- Starting CreateResources ---")
	provTopics, provSubs, err := manager.CreateResources(ctx, resources)
	require.NoError(t, err)
	assert.Len(t, provTopics, 1, "Should provision one topic")
	assert.Len(t, provSubs, 1, "Should provision one subscription")
	t.Log("--- CreateResources finished successfully ---")

	// =========================================================================
	// --- Phase 2: VERIFY Resources Exist ---
	// =========================================================================
	t.Log("--- Verifying resources exist in emulator ---")
	// Verify Topic
	topic := psClient.Topic(topicName)
	exists, err := topic.Exists(ctx)
	assert.NoError(t, err)
	assert.True(t, exists, "Pub/Sub topic should exist after creation")

	// Verify Subscription
	sub := psClient.Subscription(subName)
	exists, err = sub.Exists(ctx)
	assert.NoError(t, err)
	assert.True(t, exists, "Pub/Sub subscription should exist after creation")
	t.Log("--- Verification successful, all resources exist ---")

	// =========================================================================
	// --- Phase 3: TEARDOWN Resources ---
	// =========================================================================
	t.Log("--- Starting Teardown ---")
	err = manager.Teardown(ctx, resources)
	require.NoError(t, err, "Teardown should not fail")
	t.Log("--- Teardown finished successfully ---")

	// =========================================================================
	// --- Phase 4: VERIFY Resources are Deleted ---
	// =========================================================================
	t.Log("--- Verifying resources are deleted from emulator ---")
	// Verify Topic is gone
	exists, err = psClient.Topic(topicName).Exists(ctx)
	assert.NoError(t, err)
	assert.False(t, exists, "Pub/Sub topic should NOT exist after teardown")

	// Verify Subscription is gone
	exists, err = psClient.Subscription(subName).Exists(ctx)
	assert.NoError(t, err)
	assert.False(t, exists, "Pub/Sub subscription should NOT exist after teardown")
	t.Log("--- Deletion verification successful ---")
}
