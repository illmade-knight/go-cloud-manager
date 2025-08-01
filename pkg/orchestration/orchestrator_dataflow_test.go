//go:build integration

package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

const (
	expectedMessages = 2
	usePool          = true
)

// TestOrchestrator_DataflowE2E performs a full, end-to-end integration test of a complex dataflow.
// It deploys a ServiceDirector, which then deploys a publisher and subscriber service.
// The test verifies that the entire system is wired correctly by confirming that messages
// published by the publisher are successfully received and processed by the subscriber.
func TestOrchestrator_DataflowE2E(t *testing.T) {
	// --- Global Test Setup ---
	projectID := auth.CheckGCPAuth(t)
	require.NotEmpty(t, projectID, "GCP_PROJECT_ID environment variable must be set")

	logger := log.With().Str("test", "TestOrchestrator_DataflowE2E").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if usePool {
		t.Setenv("TEST_SA_POOL_MODE", "true")
		t.Setenv("TEST_SA_POOL_PREFIX", "it-")
	}

	// --- Arrange: Define the full architecture for the test ---
	var arch *servicemanager.MicroserviceArchitecture
	var verificationTopicName string
	var sdName string

	t.Run("Arrange - Build Architecture", func(t *testing.T) {
		region := "us-central1"
		imageRepo := "test-images"
		runID := uuid.New().String()[:8]
		sdSourcePath, _ := filepath.Abs("./testdata/toy-servicedirector")
		pubSourcePath, _ := filepath.Abs("./testdata/tracer-publisher")
		subSourcePath, _ := filepath.Abs("./testdata/tracer-subscriber")

		sdName = fmt.Sprintf("sd-%s", runID)
		pubName := fmt.Sprintf("tracer-publisher-%s", runID)
		subName := fmt.Sprintf("tracer-subscriber-%s", runID)
		commandTopicID := fmt.Sprintf("director-commands-%s", runID)
		commandSubID := fmt.Sprintf("director-command-sub-%s", runID)
		completionTopicID := fmt.Sprintf("director-events-%s", runID)
		tracerTopicName := fmt.Sprintf("tracer-topic-%s", runID)
		tracerSubName := fmt.Sprintf("tracer-sub-%s", runID)
		verificationTopicName = fmt.Sprintf("verify-topic-%s", runID)

		arch = &servicemanager.MicroserviceArchitecture{
			Environment: servicemanager.Environment{ProjectID: projectID, Region: region},
			ServiceManagerSpec: servicemanager.ServiceSpec{
				Name:           sdName,
				ServiceAccount: fmt.Sprintf("sd-sa-%s", runID),
				Deployment: &servicemanager.DeploymentSpec{
					SourcePath:          sdSourcePath,
					BuildableModulePath: ".",
					EnvironmentVars: map[string]string{
						"SD_COMMAND_TOPIC":        commandTopicID,
						"SD_COMMAND_SUBSCRIPTION": commandSubID,
						"SD_COMPLETION_TOPIC":     completionTopicID,
					},
				},
			},
			Dataflows: map[string]servicemanager.ResourceGroup{
				"tracer-flow": {
					Services: map[string]servicemanager.ServiceSpec{
						subName: {
							Name:           subName,
							ServiceAccount: fmt.Sprintf("sub-sa-%s", runID),
							Deployment: &servicemanager.DeploymentSpec{
								SourcePath:          subSourcePath,
								BuildableModulePath: ".",
								EnvironmentVars: map[string]string{
									"SUBSCRIPTION_ID": tracerSubName,
									"VERIFY_TOPIC_ID": verificationTopicName,
								},
							},
						},
						pubName: {
							Name:           pubName,
							ServiceAccount: fmt.Sprintf("pub-sa-%s", runID),
							Deployment: &servicemanager.DeploymentSpec{
								SourcePath:          pubSourcePath,
								BuildableModulePath: ".",
								EnvironmentVars:     map[string]string{"TOPIC_ID": tracerTopicName},
							},
						},
					},
					Resources: servicemanager.CloudResourcesSpec{
						Topics: []servicemanager.TopicConfig{
							{CloudResource: servicemanager.CloudResource{Name: tracerTopicName}},
							{CloudResource: servicemanager.CloudResource{Name: verificationTopicName}},
						},
						Subscriptions: []servicemanager.SubscriptionConfig{
							{CloudResource: servicemanager.CloudResource{Name: tracerSubName},
								Topic:              tracerTopicName,
								AckDeadlineSeconds: 180,
							}},
					},
				},
			},
		}
		require.NoError(t, servicemanager.HydrateArchitecture(arch, imageRepo, runID, logger))
	})

	// --- Act & Assert ---
	t.Run("Act and Assert Full Workflow", func(t *testing.T) {
		// 1. Setup Verification Listener
		psClient, err := pubsub.NewClient(ctx, projectID)
		require.NoError(t, err)
		t.Cleanup(func() { _ = psClient.Close() })

		_, verifySub := createVerificationResources(t, ctx, psClient, verificationTopicName)
		validationChan := startVerificationListener(t, ctx, verifySub, expectedMessages)

		// 2. Create Orchestrators
		iamOrch, err := orchestration.NewIAMOrchestrator(ctx, arch, logger)
		require.NoError(t, err)
		orch, err := orchestration.NewOrchestrator(ctx, arch, logger)
		require.NoError(t, err)

		// 3. Setup Unified Teardown
		t.Cleanup(func() {
			cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cCancel()
			logger.Info().Msg("--- Starting Full Teardown ---")
			if err = orch.TeardownDataflowServices(cCtx, "tracer-flow"); err != nil {
				logger.Error().Err(err).Msg("Dataflow services teardown failed")
			}
			if err = orch.TeardownCloudRunService(cCtx, sdName); err != nil {
				logger.Error().Err(err).Msg("Service director teardown failed")
			}
			if err = orch.Teardown(cCtx); err != nil {
				logger.Error().Err(err).Msg("Orchestrator teardown failed")
			}
			if err = iamOrch.Teardown(cCtx); err != nil {
				logger.Error().Err(err).Msg("IAM teardown failed")
			}
			logger.Info().Msg("--- Teardown Complete ---")
		})

		// 4. Execute Deployment Workflow
		sdSaEmails, err := iamOrch.SetupServiceDirectorIAM(ctx)
		require.NoError(t, err)
		directorURL, err := orch.DeployServiceDirector(ctx, sdSaEmails)
		require.NoError(t, err)
		err = orch.AwaitServiceReady(ctx, sdName)
		require.NoError(t, err, "Did not receive 'service_ready' event in time")
		err = orch.TriggerDataflowSetup(ctx, "tracer-flow")
		require.NoError(t, err)
		err = orch.AwaitDataflowReady(ctx, "tracer-flow")
		require.NoError(t, err)
		dfSaEmails, err := iamOrch.ApplyIAMForDataflow(ctx, "tracer-flow")
		require.NoError(t, err)

		allSaEmails := make(map[string]string)
		for k, v := range sdSaEmails {
			allSaEmails[k] = v
		}
		for k, v := range dfSaEmails {
			allSaEmails[k] = v
		}

		err = orch.DeployDataflowServices(ctx, "tracer-flow", allSaEmails, directorURL)
		require.NoError(t, err)
		t.Log("Application services deployed.")

		// 5. Verify the Live Dataflow
		t.Logf("Waiting to receive %d verification message(s)...", expectedMessages)
		select {
		case err := <-validationChan:
			require.NoError(t, err, "Verification failed while receiving messages")
			t.Logf("✅ Verification successful! Received %d message(s).", expectedMessages)
		case <-ctx.Done():
			t.Fatal("Test context timed out before verification completed.")
		}
	})
}

// createVerificationResources creates the topic and subscription used by the test to verify results.
func createVerificationResources(t *testing.T, ctx context.Context, client *pubsub.Client, topicName string) (*pubsub.Topic, *pubsub.Subscription) {
	t.Helper()
	verifyTopic, err := client.CreateTopic(ctx, topicName)
	require.NoError(t, err)

	subID := fmt.Sprintf("verify-sub-%s", uuid.New().String()[:8])
	verifySub, err := client.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic:               verifyTopic,
		AckDeadline:         20 * time.Second,
		RetainAckedMessages: true,
		RetentionDuration:   10 * time.Minute,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		t.Log("Cleaning up verification resources...")
		cleanupCtx := context.Background()
		if err := verifySub.Delete(cleanupCtx); err != nil {
			t.Logf("Error deleting verify sub: %v", err)
		}
		if err := verifyTopic.Delete(cleanupCtx); err != nil {
			t.Logf("Error deleting verify topic: %v", err)
		}
	})

	return verifyTopic, verifySub
}

// startVerificationListener starts a background goroutine to listen for messages on the verification subscription.
func startVerificationListener(t *testing.T, ctx context.Context, sub *pubsub.Subscription, expectedCount int) <-chan error {
	t.Helper()
	validationChan := make(chan error, 1)
	var receivedCount atomic.Int32

	go func() {
		err := sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
			log.Info().Str("data", string(m.Data)).Msg("Verifier received a message.")
			m.Ack()

			if receivedCount.Add(1) == int32(expectedCount) {
				validationChan <- nil // Signal success
			}
		})

		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case validationChan <- fmt.Errorf("pubsub receive failed unexpectedly: %w", err):
			default:
			}
		}
	}()

	return validationChan
}
