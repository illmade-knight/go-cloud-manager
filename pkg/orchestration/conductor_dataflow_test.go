//go:build integration

package orchestration_test

import (
	"context"
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

// in conductor_dataflow_test.go

func TestConductor_DataflowE2E(t *testing.T) {
	projectID := CheckGCPAuth(t)
	require.NotEmpty(t, projectID, "GCP_PROJECT_ID environment variable must be set")

	region := "us-central1"
	imageRepo := "test-images"
	logger := log.With().Str("test", "TestConductor_DataflowE2E").Logger()
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

	// --- 1. Arrange: Define all resources and architecture ---
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
					"VERIFICATION_TOPIC_ID":   verificationTopicName, // Ensure SD creates this topic
					"TRACER_TOPIC_ID":         tracerTopicName,
					"TRACER_SUB_ID":           tracerSubName,
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"tracer-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					pubName: {
						Name:           pubName,
						ServiceAccount: fmt.Sprintf("pub-sa-%s", runID),
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          pubSourcePath,
							BuildableModulePath: ".",
							EnvironmentVars: map[string]string{
								"TOPIC_ID":              tracerTopicName,
								"AUTO_PUBLISH_ENABLED":  "true",
								"AUTO_PUBLISH_COUNT":    fmt.Sprintf("%d", *expectedMessages),
								"AUTO_PUBLISH_INTERVAL": "1s",
							},
						},
					},
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
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics:        []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: tracerTopicName}}, {CloudResource: servicemanager.CloudResource{Name: verificationTopicName}}},
					Subscriptions: []servicemanager.SubscriptionConfig{{CloudResource: servicemanager.CloudResource{Name: tracerSubName}, Topic: tracerTopicName}},
				},
			},
		},
	}
	require.NoError(t, servicemanager.HydrateArchitecture(arch, imageRepo, ""))

	// --- 2. Setup Verification and Conductor ---
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer psClient.Close()

	verifyTopic, verifySub := createVerificationResources(t, ctx, psClient, verificationTopicName)

	t.Logf("verify topic %s", verifyTopic.ID())
	t.Logf("verify sub %s", verifySub.ID())

	validationChan := startVerificationListener(t, ctx, verifySub, *expectedMessages)

	conductor, err := orchestration.NewConductor(ctx, arch, logger)
	require.NoError(t, err)

	// --- Teardown Logic ---
	t.Cleanup(func() {
		cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cCancel()
		t.Log("--- Starting Full Teardown ---")

		// Create a temporary orchestrator just to access the teardown helpers.
		// This ensures deployed services are removed.
		cleanupOrch, _ := orchestration.NewOrchestrator(cCtx, arch, logger)
		if cleanupOrch != nil {
			if err := cleanupOrch.TeardownDataflowServices(cCtx, "tracer-flow"); err != nil {
				t.Logf("Dataflow services teardown failed: %v", err)
			}
			if err := cleanupOrch.TeardownCloudRunService(cCtx, sdName); err != nil {
				t.Logf("Service director teardown failed: %v", err)
			}
		}

		// Teardown the conductor's own resources (IAM, internal topics).
		if err := conductor.Teardown(cCtx); err != nil {
			t.Errorf("Conductor teardown failed: %v", err)
		}
		t.Log("--- Teardown Complete ---")
	})

	// --- 3. Execute Conductor Workflow ---
	err = conductor.Run(ctx)
	require.NoError(t, err)
	t.Log("Conductor has finished the deployment workflow.")

	// --- 4. Verify the Live Dataflow ---
	t.Logf("Waiting to receive %d verification message(s)...", *expectedMessages)
	select {
	case err := <-validationChan:
		require.NoError(t, err, "Verification failed while receiving messages")
		t.Logf("✅ Verification successful! Received %d message(s).", *expectedMessages)
	case <-time.After(3 * time.Minute):
		t.Fatalf("Timeout: Did not complete verification for %d messages in time.", *expectedMessages)
	}
}
