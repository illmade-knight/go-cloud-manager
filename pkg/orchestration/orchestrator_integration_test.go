//go:build integration

package orchestration_test

import (
	"context"
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
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// mockIAMClient is a no-op mock that satisfies the iam.IAMClient interface.
// It is used to isolate the ServiceDirector from real cloud APIs in this test.
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
	return nil
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
func (m *mockIAMClient) AddMemberToServiceAccountRole(ctx context.Context, a, b, c string) error {
	return nil
}
func (m *mockIAMClient) Close() error { return nil }

// TestOrchestratorCommandFlow verifies the full asynchronous command-and-reply loop
// between the Orchestrator and an in-memory ServiceDirector using Pub/Sub emulators.
func TestOrchestratorCommandFlow(t *testing.T) {
	// --- Arrange ---
	logger := log.With().Str("test", "TestOrchestratorCommandFlow").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "test-harness-project"
	commandTopicID := "director-commands-harness"
	commandSubID := "director-command-sub-harness"
	completionTopicID := "director-events-harness"
	dataflowTopicToCreate := "e2e-dataflow-topic"

	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "in-memory-director",
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
				Services: map[string]servicemanager.ServiceSpec{
					"producer-service": {ServiceAccount: "producer-sa"},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: dataflowTopicToCreate}, ProducerService: &servicemanager.ServiceMapping{Name: "producer-service"}},
					},
				},
			},
		},
	}

	orch, err := orchestration.NewOrchestratorWithClients(ctx, arch, psClient, nil, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = orch.Teardown(context.Background()) })

	require.NoError(t, orchestration.WaitForState(ctx, orch, orchestration.StateCommandInfraReady), "Orchestrator failed to initialize")
	t.Log("Orchestrator is ready.")

	t.Log("Starting in-memory ServiceDirector...")
	directorCfg := &servicedirector.Config{
		BaseConfig: microservice.BaseConfig{ProjectID: projectID},
		Commands: &servicedirector.PubsubConfig{
			CommandTopicID:    commandTopicID,
			CommandSubID:      commandSubID,
			CompletionTopicID: completionTopicID,
			Options:           pubsubConn.ClientOptions,
		},
	}

	messagingClient := servicemanager.MessagingClientFromPubsubClient(psClient)
	sm, err := servicemanager.NewServiceManagerFromClients(messagingClient, nil, nil, arch.Environment, nil, logger)
	require.NoError(t, err)

	mockIamClient := &mockIAMClient{}
	iamManager, err := iam.NewIAMManager(mockIamClient, logger)
	require.NoError(t, err)

	director, err := servicedirector.NewDirectServiceDirector(ctx, directorCfg, arch, sm, iamManager, mockIamClient, psClient, logger)
	require.NoError(t, err)

	go func() {
		if err := director.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("director.Start() returned an unexpected error: %v", err)
		}
	}()
	t.Cleanup(func() { director.Shutdown(context.Background()) })
	<-director.Ready()

	// --- Act & Assert ---
	err = orch.AwaitServiceReady(ctx, arch.ServiceManagerSpec.Name)
	require.NoError(t, err, "Did not receive 'service_ready' event from in-memory director")
	t.Log("Orchestrator confirmed ServiceDirector is ready.")

	err = orch.TriggerDataflowResourceCreation(ctx, "test-flow")
	require.NoError(t, err)

	_, err = orch.AwaitDataflowReady(ctx, "test-flow")
	require.NoError(t, err, "Did not receive completion event in time")

	t.Log("Verifying that the dataflow resource was created in the emulator...")
	topic := psClient.Topic(dataflowTopicToCreate)
	exists, err := topic.Exists(ctx)
	require.NoError(t, err, "Failed to check for new topic existence")
	require.True(t, exists, "The ServiceDirector should have created the dataflow topic in the emulator")
	t.Log("✅ Resource creation verified. Test successful.")
}
