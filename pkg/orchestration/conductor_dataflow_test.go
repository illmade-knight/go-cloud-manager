//go:build integration

package orchestration_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/idtoken"
)

func TestConductor_DataflowE2E(t *testing.T) {
	projectID := os.Getenv("GCP_PROJECT_ID")
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
					pubName: {
						Name:           pubName,
						ServiceAccount: fmt.Sprintf("pub-sa-%s", runID),
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          pubSourcePath,
							BuildableModulePath: ".",
							EnvironmentVars:     map[string]string{"TOPIC_ID": tracerTopicName},
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
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: tracerTopicName}},
						{
							CloudResource: servicemanager.CloudResource{Name: verificationTopicName},
						},
					},
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
	resultChan, verifySub := setupVerificationListener(t, ctx, psClient, verificationTopicName, *expectedMessages)
	t.Cleanup(func() { _ = verifySub.Delete(context.Background()) })

	conductor, err := orchestration.NewConductor(ctx, arch, logger)
	require.NoError(t, err)

	t.Cleanup(func() {
		cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cCancel()
		if err := conductor.Teardown(cCtx); err != nil {
			t.Errorf("Conductor teardown failed: %v", err)
		}
	})

	// --- 3. Execute Conductor Workflow ---
	err = conductor.Run(ctx)
	require.NoError(t, err)
	t.Log("Conductor has finished the deployment workflow.")

	// --- 4. Verify the Live Dataflow ---
	traceID := uuid.New().String()
	publisherSvcSpec := arch.Dataflows["tracer-flow"].Services[pubName]

	// We need a fresh orchestrator just to get the AwaitRevisionReady helper.
	// In a real CLI, this might be part of a separate "status" command.
	verifyOrch, err := orchestration.NewOrchestrator(ctx, arch, logger)
	require.NoError(t, err)
	defer verifyOrch.Teardown(ctx) // Teardown its internal resources

	require.NoError(t, verifyOrch.AwaitRevisionReady(ctx, publisherSvcSpec), "Tracer Publisher never became ready")

	authClient, err := idtoken.NewClient(ctx, publisherSvcSpec.Deployment.ServiceURL)
	require.NoError(t, err, "Failed to create authenticated client")

	resp, err := authClient.Post(fmt.Sprintf("%s?trace_id=%s", publisherSvcSpec.Deployment.ServiceURL, traceID), "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	t.Logf("Successfully sent tracer message with ID: %s", traceID)

	select {
	case receivedID := <-resultChan:
		require.Equal(t, traceID, receivedID, "Received trace ID does not match sent ID")
		t.Logf("✅ Verification successful! Received tracer ID %s on verification subscription.", receivedID)
	case <-time.After(2 * time.Minute):
		t.Fatal("Timeout: Did not receive verification message in time.")
	}
}
