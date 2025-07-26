//go:build integration

package orchestration_test

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

var expectedCMessages = flag.Int("expected-messages", 2, "Number of messages the verifier should expect to receive.")
var useCPool = flag.Bool("use-pool", false, "Use a pool of service accounts for testing to avoid quota issues.")

func TestOrchestrator_DataflowE2E_WithConductor(t *testing.T) {
	projectID := CheckGCPAuth(t)
	require.NotEmpty(t, projectID, "GCP_PROJECT_ID environment variable must be set")

	region := "us-central1"
	imageRepo := "test-images"
	logger := log.With().Str("test", "TestOrchestrator_DataflowE2E_WithConductor").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if *useCPool {
		t.Setenv("TEST_SA_POOL_MODE", "true")
		t.Setenv("TEST_SA_POOL_PREFIX", "it-")
	}

	// --- 1. Arrange: Define the full architecture for the test ---
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
	require.NoError(t, servicemanager.HydrateArchitecture(arch, imageRepo, runID))

	// --- 2. Setup Verification Listener ---
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer psClient.Close()

	_, verifySub := createVerificationResources(t, ctx, psClient, verificationTopicName)
	validationChan := startVerificationListener(t, ctx, verifySub, *expectedCMessages)

	// --- 3. Create and Run the Conductor ---
	// The Conductor now encapsulates the entire setup and deployment flow.
	conductor, err := orchestration.NewConductor(ctx, arch, logger)
	require.NoError(t, err)

	t.Cleanup(func() {
		cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cCancel()
		logger.Info().Msg("--- Starting Conductor Teardown ---")
		if err := conductor.Teardown(cCtx); err != nil {
			logger.Error().Err(err).Msg("Conductor teardown failed")
		}
		logger.Info().Msg("--- Conductor Teardown Complete ---")
	})

	// The entire multi-step deployment process is now a single, clean call.
	err = conductor.Run(ctx)
	require.NoError(t, err, "Conductor.Run() should complete without errors")

	// --- 4. Verify the Live Dataflow ---
	t.Logf("Waiting to receive %d verification message(s)...", *expectedCMessages)
	select {
	case err := <-validationChan:
		require.NoError(t, err, "Verification failed while receiving messages")
		t.Logf("✅ Verification successful! Received %d message(s).", *expectedCMessages)
	case <-time.After(5 * time.Minute):
		t.Fatalf("Timeout: Did not complete verification for %d messages in time.", *expectedCMessages)
	}
}
