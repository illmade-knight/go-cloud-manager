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
	"github.com/stretchr/testify/require"
)

// mockIAMClient is a no-op implementation for testing the ServiceDirector's flow.
// It allows us to verify resource creation without needing real IAM credentials.
type mockIAMClient struct{}

func (m *mockIAMClient) EnsureServiceAccountExists(_ context.Context, accountName string) (string, error) {
	return "mock-" + accountName + "@project.iam.gserviceaccount.com", nil
}
func (m *mockIAMClient) AddResourceIAMBinding(_ context.Context, _ iam.IAMBinding, _ string) error {
	return nil // Assume success
}
func (m *mockIAMClient) RemoveResourceIAMBinding(_ context.Context, _ iam.IAMBinding, _ string) error {
	return nil
}
func (m *mockIAMClient) CheckResourceIAMBinding(_ context.Context, _ iam.IAMBinding, _ string) (bool, error) {
	return true, nil
}
func (m *mockIAMClient) AddArtifactRegistryRepositoryIAMBinding(_ context.Context, _, _, _, _ string) error {
	return nil
}
func (m *mockIAMClient) DeleteServiceAccount(ctx context.Context, accountName string) error {
	return nil
}
func (m *mockIAMClient) AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error {
	return nil
}
func (m *mockIAMClient) Close() error { return nil }

// TestServiceDirector_Integration verifies that the director correctly processes a 'setup'
// command, creating resources and applying IAM policies.
func TestServiceDirector_Integration(t *testing.T) {
	// --- Arrange ---
	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "sd-test-project"
	commandTopicID := "sd-test-commands"
	commandSubID := "sd-test-sub"
	completionTopicID := "sd-test-events"
	dataflowTopicToCreate := "new-dataflow-topic"

	// 1. Setup Pub/Sub Emulator
	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	// Pre-create the topics the orchestrator would normally create.
	_, err = psClient.CreateTopic(ctx, commandTopicID)
	require.NoError(t, err)
	_, err = psClient.CreateTopic(ctx, completionTopicID)
	require.NoError(t, err)

	// 2. Define the Test Architecture
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "test-director",
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"test-service": {
						ServiceAccount: "test-sa",
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: dataflowTopicToCreate}},
					},
				},
			},
		},
	}

	// 3. Create ServiceDirector with Mocks and Emulator Clients
	directorCfg := &servicedirector.Config{
		BaseConfig: microservice.BaseConfig{ProjectID: projectID},
		Commands: &servicedirector.PubsubConfig{
			CommandTopicID:    commandTopicID,
			CommandSubID:      commandSubID,
			CompletionTopicID: completionTopicID,
		},
	}

	mockIamClient := &mockIAMClient{}
	iamManager, err := iam.NewIAMManager(mockIamClient, logger)
	require.NoError(t, err)

	messagingClient := servicemanager.MessagingClientFromPubsubClient(psClient)
	sm, err := servicemanager.NewServiceManagerFromClients(messagingClient, nil, nil, arch.Environment, nil, logger)
	require.NoError(t, err)

	director, err := servicedirector.NewDirectServiceDirector(ctx, directorCfg, arch, sm, iamManager, mockIamClient, psClient, logger)
	require.NoError(t, err)
	go func() {
		// We don't require.NoError here because a successful shutdown will return http.ErrServerClosed,
		// which we don't want to fail the test on.
		if err := director.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("director.Start() returned an unexpected error: %v", err)
		}
	}()
	t.Cleanup(func() { director.Shutdown(context.Background()) })

	// --- Act ---
	t.Log("Publishing 'setup' command to ServiceDirector...")
	cmd := orchestration.Command{
		Instruction: orchestration.Setup,
		Value:       "test-flow",
	}
	cmdData, err := json.Marshal(cmd)
	require.NoError(t, err)

	// Listen for the completion event to know when the Director is done.
	completionSubID := "test-completion-listener"
	completionSub, err := psClient.CreateSubscription(ctx, completionSubID, pubsub.SubscriptionConfig{Topic: psClient.Topic(completionTopicID)})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		_ = completionSub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
			msg.Ack()
			var event orchestration.CompletionEvent
			_ = json.Unmarshal(msg.Data, &event)
			if event.Value == "test-flow" && event.Status == orchestration.DataflowComplete {
				close(done)
			}
		})
	}()

	// Publish the command to trigger the setup.
	psClient.Topic(commandTopicID).Publish(ctx, &pubsub.Message{Data: cmdData})

	// Wait for the completion signal or timeout.
	select {
	case <-done:
		t.Log("Received completion event from ServiceDirector.")
	case <-ctx.Done():
		t.Fatal("Test timed out waiting for completion event from ServiceDirector")
	}

	// --- Assert ---
	t.Log("Verifying that the dataflow resource was created in the emulator...")
	topic := psClient.Topic(dataflowTopicToCreate)
	exists, err := topic.Exists(ctx)
	require.NoError(t, err, "Failed to check for new topic existence")
	require.True(t, exists, "The ServiceDirector should have created the dataflow topic in the emulator")
	t.Log("✅ Resource creation verified. Test successful.")
}
