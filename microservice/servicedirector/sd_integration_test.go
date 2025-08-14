//go:build integration

package servicedirector_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
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

// mockIAMClient is a "spy" that records calls to ApplyIAMPolicy for verification.
// It fully implements the iam.IAMClient interface for use in tests.
type mockIAMClient struct {
	mu              sync.Mutex
	AppliedPolicies []iam.PolicyBinding
}

func (m *mockIAMClient) ApplyIAMPolicy(ctx context.Context, binding iam.PolicyBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AppliedPolicies = append(m.AppliedPolicies, binding)
	return nil
}

func (m *mockIAMClient) GetServiceAccount(ctx context.Context, accountEmail string) error {
	return nil // Simulate success, account exists.
}

func (m *mockIAMClient) EnsureServiceAccountExists(_ context.Context, accountName string) (string, error) {
	return "mock-" + accountName + "@project.iam.gserviceaccount.com", nil
}
func (m *mockIAMClient) AddResourceIAMBinding(_ context.Context, _ iam.IAMBinding, _ string) error {
	return nil
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
// command, creating resources AND applying the correct IAM policies via the IAMManager.
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

	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	_, err = psClient.CreateTopic(ctx, commandTopicID)
	require.NoError(t, err)
	_, err = psClient.CreateTopic(ctx, completionTopicID)
	require.NoError(t, err)

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"producer-service": {ServiceAccount: "producer-sa"},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: dataflowTopicToCreate},
							ProducerService: &servicemanager.ServiceMapping{Name: "producer-service"},
						},
					},
				},
			},
		},
	}

	directorCfg := &servicedirector.Config{
		BaseConfig: microservice.BaseConfig{ProjectID: projectID},
		Commands: &servicedirector.PubsubConfig{
			CommandTopicID:    commandTopicID,
			CommandSubID:      commandSubID,
			CompletionTopicID: completionTopicID,
			Options:           pubsubConn.ClientOptions,
		},
	}

	mockIamClient := &mockIAMClient{}
	// REFACTOR: Create the IAMManager, which is now the required dependency.
	iamManager, err := iam.NewIAMManager(mockIamClient, logger)
	require.NoError(t, err)

	messagingClient := servicemanager.MessagingClientFromPubsubClient(psClient)
	sm, err := servicemanager.NewServiceManagerFromClients(messagingClient, nil, nil, nil, arch.Environment, nil, logger)
	require.NoError(t, err)

	// REFACTOR: Pass the IAMManager to the updated constructor.
	director, err := servicedirector.NewDirectServiceDirector(ctx, directorCfg, arch, sm, iamManager, mockIamClient, psClient, logger)
	require.NoError(t, err)

	go func() {
		if err := director.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("director.Start() returned an unexpected error: %v", err)
		}
	}()
	t.Cleanup(func() { director.Shutdown(context.Background()) })
	<-director.Ready()

	// --- Act ---
	t.Log("Publishing 'setup' command to ServiceDirector...")
	cmd := orchestration.Command{
		Instruction: orchestration.Setup,
		Value:       "test-flow",
	}
	cmdData, err := json.Marshal(cmd)
	require.NoError(t, err)

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

	psClient.Topic(commandTopicID).Publish(ctx, &pubsub.Message{Data: cmdData})

	select {
	case <-done:
		t.Log("Received completion event from ServiceDirector.")
	case <-ctx.Done():
		t.Fatal("Test timed out waiting for completion event from ServiceDirector")
	}

	// --- Assert ---
	t.Run("Verify Resource Creation", func(t *testing.T) {
		t.Log("Verifying that the dataflow resource was created in the emulator...")
		topic := psClient.Topic(dataflowTopicToCreate)
		exists, err := topic.Exists(ctx)
		require.NoError(t, err, "Failed to check for new topic existence")
		require.True(t, exists, "The ServiceDirector should have created the dataflow topic")
		t.Log("✅ Resource creation verified.")
	})

	t.Run("Verify IAM Policy Application", func(t *testing.T) {
		t.Log("Verifying that the correct IAM policies were applied...")
		require.Len(t, mockIamClient.AppliedPolicies, 1, "Expected ApplyIAMPolicy to be called once for the topic")

		policyForTopic := mockIamClient.AppliedPolicies[0]
		require.Equal(t, "pubsub_topic", policyForTopic.ResourceType)
		require.Equal(t, dataflowTopicToCreate, policyForTopic.ResourceID)

		expectedMember := "serviceAccount:mock-producer-sa@project.iam.gserviceaccount.com"
		require.Contains(t, policyForTopic.MemberRoles["roles/pubsub.publisher"], expectedMember)
		require.Contains(t, policyForTopic.MemberRoles["roles/pubsub.viewer"], expectedMember)
		t.Log("✅ IAM policy application verified.")
	})
}
