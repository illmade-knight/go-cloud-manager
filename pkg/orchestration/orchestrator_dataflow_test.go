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
	region := "eu-central-1"
	imageRepo := "test-images"

	logger := log.With().Str("test", "TestOrchestrator_DataflowE2E").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// --- 1. Arrange ---
	runID := uuid.New().String()[:8]

	// Get absolute paths to our testdata applications
	sdSourcePath, _ := filepath.Abs("./testdata/toy-servicedirector")
	pubSourcePath, _ := filepath.Abs("./testdata/tracer-publisher")
	subSourcePath, _ := filepath.Abs("./testdata/tracer-subscriber")

	// Define names for all unique resources for this test run
	sdName := fmt.Sprintf("sd-%s", runID)
	pubName := fmt.Sprintf("tracer-publisher-%s", runID)
	subName := fmt.Sprintf("tracer-subscriber-%s", runID)
	commandTopicID := fmt.Sprintf("director-commands-%s", runID)
	commandSubID := fmt.Sprintf("director-command-sub-%s", runID)
	completionTopicID := fmt.Sprintf("director-events-%s", runID)
	tracerTopicName := fmt.Sprintf("tracer-topic-%s", runID)
	tracerSubName := fmt.Sprintf("tracer-sub-%s", runID)

	// Define the full architecture for the test.
	arch := &servicemanager.MicroserviceArchitecture{
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
					pubName: {
						ServiceAccount: fmt.Sprintf("pub-sa-%s", runID),
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          pubSourcePath,
							BuildableModulePath: ".",
							EnvironmentVars:     map[string]string{"TOPIC_ID": tracerTopicName},
						},
					},
					subName: {
						ServiceAccount: fmt.Sprintf("sub-sa-%s", runID),
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          subSourcePath,
							BuildableModulePath: ".",
							EnvironmentVars:     map[string]string{"SUBSCRIPTION_ID": tracerSubName},
						},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics:        []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: tracerTopicName}}},
					Subscriptions: []servicemanager.SubscriptionConfig{{CloudResource: servicemanager.CloudResource{Name: tracerSubName}, Topic: tracerTopicName}},
				},
			},
		},
	}
	require.NoError(t, servicemanager.HydrateArchitecture(arch, imageRepo, ""))

	// --- 2. Create Orchestrator and Run Full Workflow ---
	orch, err := orchestration.NewOrchestrator(ctx, arch, logger)
	require.NoError(t, err)
	defer orch.Close()

	iamOrch, err := orchestration.NewIAMOrchestrator(ctx, arch, logger)
	require.NoError(t, err)

	// Step 2a: Setup IAM
	sdSaEmail, err := iamOrch.SetupServiceDirectorIAM(ctx)
	require.NoError(t, err)
	err = iamOrch.SetupDataflowIAM(ctx)
	require.NoError(t, err)

	// Step 2b: Deploy Director and WAIT for it to be ready via Pub/Sub event
	directorURL, err := orch.DeployServiceDirector(ctx, sdSaEmail)
	require.NoError(t, err)
	t.Logf("Waiting for 'service_ready' event from %s...", sdName)
	err = orch.AwaitServiceReady(ctx, sdName)
	require.NoError(t, err, "Did not receive 'service_ready' event in time")
	t.Log("ServiceDirector is confirmed ready via Pub/Sub event.")

	// Step 2c: Trigger and Await Dataflow Resource Creation
	err = orch.TriggerDataflowSetup(ctx, "tracer-flow")
	require.NoError(t, err)
	err = orch.AwaitDataflowReady(ctx, "tracer-flow")
	require.NoError(t, err)
	t.Log("Dataflow resource setup complete.")

	// Step 2d: Deploy the application services
	err = orch.DeployDataflowServices(ctx, "tracer-flow", directorURL)
	require.NoError(t, err)
	t.Log("Application services deployed.")

	// --- 3. Verify the Live Dataflow ---
	t.Log("All services deployed. Performing live dataflow verification...")
	traceID := uuid.New().String()

	publisherSvcSpec := arch.Dataflows["tracer-flow"].Services[pubName]
	require.NoError(t, orch.AwaitRevisionReady(ctx, publisherSvcSpec), "Tracer Publisher never became ready")
	publisherURL := publisherSvcSpec.Deployment.Image

	resp, err := http.Post(fmt.Sprintf("%s?trace_id=%s", publisherURL, traceID), "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	t.Logf("Successfully sent tracer message with ID: %s", traceID)

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
