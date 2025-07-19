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

	"cloud.google.com/go/logging/logadmin"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
)

// TestOrchestrator_DataflowE2E performs a full, real-world deployment of a multi-service
// dataflow, and verifies it by passing a tracer message through the live system.
func TestOrchestrator_DataflowE2E(t *testing.T) {
	projectID := os.Getenv("GCP_PROJECT_ID")

	if projectID == "" {
		t.Skip("Skipping cloud integration test: -project-id flag or GCP_PROJECT_ID env var must be set.")
	}

	region := "eu-central-1"
	imageRepo := "test-images"

	logger := log.With().Str("test", "TestOrchestrator_DataflowE2E").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	// --- 1. Arrange ---
	runID := uuid.New().String()[:8]

	// Get absolute paths to our testdata applications
	sdSourcePath, _ := filepath.Abs("./testdata/toyservicedirector")
	pubSourcePath, _ := filepath.Abs("./testdata/tracerpub")
	subSourcePath, _ := filepath.Abs("./testdata/tracersub")

	// Define names for all unique resources for this test run
	sdName := fmt.Sprintf("sd-%s", runID)
	pubName := fmt.Sprintf("tracer-publisher-%s", runID)
	subName := fmt.Sprintf("tracer-subscriber-%s", runID)
	commandTopicID := fmt.Sprintf("director-commands-%s", runID)
	commandSubID := fmt.Sprintf("director-command-sub-%s", runID)
	completionTopicID := fmt.Sprintf("director-events-%s", runID)
	tracerTopicID := fmt.Sprintf("tracer-topic-%s", runID)
	tracerSubID := fmt.Sprintf("tracer-sub-%s", runID)

	// Define the full architecture for the test.
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Region: region},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           sdName,
			ServiceAccount: fmt.Sprintf("sd-sa-%s", runID),
			Deployment: &servicemanager.DeploymentSpec{
				SourcePath:          sdSourcePath,
				BuildableModulePath: ".",
				// THIS IS THE KEY FIX: Provide all environment variables the toy director needs.
				EnvironmentVars: map[string]string{
					"PROJECT_ID":              projectID,
					"SD_COMMAND_TOPIC":        commandTopicID,
					"SD_COMMAND_SUBSCRIPTION": commandSubID,
					"SD_COMPLETION_TOPIC":     completionTopicID,
					"TRACER_TOPIC_ID":         tracerTopicID,
					"TRACER_SUB_ID":           tracerSubID,
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"tracer-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					pubName: {
						ServiceAccount: fmt.Sprintf("pub-sa-%s", runID),
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          pubSourcePath,
							BuildableModulePath: ".",
							EnvironmentVars: map[string]string{
								"TOPIC_ID": tracerTopicID,
							},
						},
					},
					subName: {
						ServiceAccount: fmt.Sprintf("sub-sa-%s", runID),
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          subSourcePath,
							BuildableModulePath: ".",
							EnvironmentVars: map[string]string{
								"SUBSCRIPTION_ID": tracerSubID,
							},
						},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics:        []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: tracerTopicID}}},
					Subscriptions: []servicemanager.SubscriptionConfig{{CloudResource: servicemanager.CloudResource{Name: tracerSubID}, Topic: tracerTopicID}},
				},
			},
		},
	}
	require.NoError(t, servicemanager.HydrateArchitecture(arch, imageRepo, ""))

	// --- 2. Create Orchestrator and Run Full Workflow ---
	orch, _ := orchestration.NewOrchestrator(ctx, arch, logger)
	defer orch.Close()
	iamOrch, _ := orchestration.NewIAMOrchestrator(ctx, arch, logger)

	sdSaEmail, _ := iamOrch.SetupServiceDirectorIAM(ctx)
	iamOrch.SetupDataflowIAM(ctx)
	directorURL, _ := orch.DeployServiceDirector(ctx, sdSaEmail)
	orch.TriggerDataflowSetup(ctx, "tracer-flow")
	orch.AwaitDataflowReady(ctx, "tracer-flow")
	orch.DeployDataflowServices(ctx, "tracer-flow", directorURL)

	// --- 3. Verify the Live Dataflow ---
	t.Log("All services deployed. Performing live dataflow verification...")
	traceID := uuid.New().String()

	publisherSvcSpec := arch.Dataflows["tracer-flow"].Services[pubName]
	publisherURL := publisherSvcSpec.Deployment.Image // Image field is replaced by service URL after deploy
	require.NotEmpty(t, publisherURL, "Publisher URL should not be empty after deployment")

	// Send the tracer message via an HTTP request.
	resp, err := http.Post(fmt.Sprintf("%s?trace_id=%s", publisherURL, traceID), "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	t.Logf("Successfully sent tracer message with ID: %s", traceID)

	// Poll the logs of the subscriber service until the tracer message appears.
	require.Eventually(t, func() bool {
		found, err := checkCloudRunLogs(ctx, projectID, subName, traceID)
		if err != nil {
			t.Logf("Log check warning: %v", err)
			return false
		}
		return found
	}, 2*time.Minute, 5*time.Second, "Tracer message with ID %s never appeared in subscriber logs", traceID)

	t.Logf("✅ Verification successful! Found tracer ID %s in subscriber logs.", traceID)
}

// checkCloudRunLogs queries Cloud Logging for a specific string.
func checkCloudRunLogs(ctx context.Context, projectID, serviceName, traceID string) (bool, error) {
	client, err := logadmin.NewClient(ctx, projectID)
	if err != nil {
		return false, err
	}
	defer client.Close()

	filter := fmt.Sprintf(`resource.type="cloud_run_revision" resource.labels.service_name="%s" textPayload:"%s"`, serviceName, traceID)
	it := client.Entries(ctx, logadmin.Filter(filter))

	_, err = it.Next()
	if err == iterator.Done {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil // Found it
}
