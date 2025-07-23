//go:build integration

package orchestration_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// Add this alongside the existing 'usePool' flag
var expectedMessages = flag.Int("expected-messages", 1, "Number of messages the verifier should expect to receive.")

// Define a flag to enable the service account pool for testing.
var usePool = flag.Bool("use-pool", false, "Use a pool of service accounts for testing to avoid quota issues.")

func TestOrchestrator_DataflowE2E(t *testing.T) {
	projectID := CheckGCPAuth(t)
	require.NotEmpty(t, projectID, "GCP_PROJECT_ID environment variable must be set")

	region := "us-central1"
	imageRepo := "test-images"
	logger := log.With().Str("test", "TestOrchestrator_DataflowE2E").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if *usePool {
		logger.Info().Msg("✅ Test running in POOLED mode.")
		t.Setenv("TEST_SA_POOL_MODE", "true")
		t.Setenv("TEST_SA_POOL_PREFIX", "it-")
		t.Setenv("TEST_SA_POOL_NO_CREATE", "true")
	} else {
		logger.Info().Msg("Running in STANDARD mode (creating new service accounts).")
	}

	// --- 1. Arrange ---
	runID := uuid.New().String()[:8]
	sdSourcePath, _ := filepath.Abs("./testdata/toy-servicedirector")
	pubSourcePath, _ := filepath.Abs("./testdata/tracer-publisher")
	subSourcePath, _ := filepath.Abs("./testdata/tracer-subscriber")

	sdName := fmt.Sprintf("sd-%s", runID)
	pubName := fmt.Sprintf("tracer-publisher-%s", runID)
	subName := fmt.Sprintf("tracer-subscriber-%s", runID)
	commandTopicID := fmt.Sprintf("director-commands-%s", runID)
	commandSubID := fmt.Sprintf("director-command-sub-%s", runID)
	completionTopicID := fmt.Sprintf("director-events-%s", runID)
	tracerTopicName := fmt.Sprintf("tracer-topic-%s", runID)
	tracerSubName := fmt.Sprintf("tracer-sub-%s", runID)
	verificationTopicName := fmt.Sprintf("verify-topic-%s", runID)

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Region: region},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           sdName,
			ServiceAccount: fmt.Sprintf("sd-sa-%s", runID),
			Deployment: &servicemanager.DeploymentSpec{
				SourcePath:          sdSourcePath,
				BuildableModulePath: ".",
				EnvironmentVars: map[string]string{
					"PROJECT_ID":              projectID,
					"SD_COMMAND_TOPIC":        commandTopicID,
					"SD_COMMAND_SUBSCRIPTION": commandSubID,
					"SD_COMPLETION_TOPIC":     completionTopicID,
					"TRACER_TOPIC_ID":         tracerTopicName,
					"TRACER_SUB_ID":           tracerSubName,
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
						{
							CloudResource: servicemanager.CloudResource{Name: verificationTopicName},
						},
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
	require.NoError(t, servicemanager.HydrateArchitecture(arch, imageRepo, ""))

	// --- 2. Setup Verification and Orchestrators ---
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer psClient.Close()

	// Create the verifier's resources, but don't start listening yet.
	verifyTopic, verifySub := createVerificationResources(t, ctx, psClient, verificationTopicName)

	t.Logf("verify topic %s", verifyTopic.ID())
	t.Logf("verify sub %s", verifySub.ID())

	// Now that we've confirmed the subscription works, start the main listener.
	validationChan := startVerificationListener(t, ctx, verifySub, *expectedMessages)

	t.Cleanup(func() { _ = verifySub.Delete(context.Background()) }) // Ensure subscription is deleted

	iamOrch, err := orchestration.NewIAMOrchestrator(ctx, arch, logger)
	require.NoError(t, err)
	orch, err := orchestration.NewOrchestrator(ctx, arch, logger)
	require.NoError(t, err)

	t.Cleanup(func() {
		cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cCancel()
		logger.Info().Msg("--- Starting Full Teardown ---")

		// Tear down all services with the new, clear functions.
		if err := orch.TeardownDataflowServices(cCtx, "tracer-flow"); err != nil {
			logger.Error().Err(err).Msg("Dataflow services teardown failed")
		}
		if err := orch.TeardownCloudRunService(cCtx, sdName); err != nil {
			logger.Error().Err(err).Msg("Service director teardown failed")
		}

		// Teardown orchestrator and IAM resources
		if err := orch.Teardown(cCtx); err != nil {
			logger.Error().Err(err).Msg("Orchestrator teardown failed")
		}
		if err := iamOrch.Teardown(cCtx); err != nil {
			logger.Error().Err(err).Msg("IAM teardown failed")
		}
		logger.Info().Msg("--- Teardown Complete ---")
	})

	// --- 3. Execute Deployment Workflow ---
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
	dfSaEmails, err := iamOrch.SetupDataflowIAM(ctx)
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

	// --- 4. Verify the Live Dataflow ---
	// The old verification logic is replaced with this new block.
	t.Logf("Waiting to receive %d verification message(s)...", *expectedMessages)
	select {
	case err := <-validationChan:
		require.NoError(t, err, "Verification failed while receiving messages")
		t.Logf("✅ Verification successful! Received %d message(s).", *expectedMessages)
	case <-time.After(5 * time.Minute): // An overall safety timeout for the test.
		t.Fatalf("Timeout: Did not complete verification for %d messages in time.", *expectedMessages)
	}
}

// in orchestrator_dataflow_test.go

// createVerificationResources just creates the topic and subscription.
func createVerificationResources(t *testing.T, ctx context.Context, client *pubsub.Client, topicName string) (*pubsub.Topic, *pubsub.Subscription) {
	t.Helper()
	verifyTopic, err := client.CreateTopic(ctx, topicName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = verifyTopic.Delete(context.Background()) })

	subID := fmt.Sprintf("verify-sub-%s", uuid.New().String()[:8])
	verifySub, err := client.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic:               verifyTopic,
		AckDeadline:         20 * time.Second,
		RetainAckedMessages: true,
		RetentionDuration:   10 * time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = verifySub.Delete(context.Background()) })

	return verifyTopic, verifySub
}

// in startVerificationListener()
func startVerificationListener(t *testing.T, ctx context.Context, sub *pubsub.Subscription, expectedCount int) <-chan error {
	t.Helper()
	validationChan := make(chan error, 1)
	var receivedCount atomic.Int32

	go func() {
		cctx, cancel := context.WithCancel(ctx)
		// IMPORTANT do NOT place a defer cancel in this block - subscription recovers from errors a defer cancel will prevent this

		err := sub.Receive(cctx, func(ctx context.Context, m *pubsub.Message) {
			log.Info().Str("data", string(m.Data)).Msg("Verifier received a message.")
			m.Ack()

			if expectedCount > 1 && !strings.HasPrefix(string(m.Data), "iot-trace-") {
				select {
				case validationChan <- fmt.Errorf("received message with invalid format: %s", string(m.Data)):
				default:
				}
				cancel()
				return
			}
			if receivedCount.Add(1) == int32(expectedCount) {
				select {
				case validationChan <- nil:
				default:
				}
				cancel()
			}
		})

		// This error handling is now more important. If the main test context is
		// cancelled, this will exit gracefully.
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case validationChan <- fmt.Errorf("pubsub receive failed unexpectedly: %w", err):
			default:
			}
		}
	}()

	return validationChan
}
