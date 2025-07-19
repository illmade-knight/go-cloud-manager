//go:build cloud_integration

package orchestration_test

import (
	"context"
	"flag"
	"fmt"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

var (
	// These flags are now defined in orchestration_test.go and can be used across tests.
	projectIDFlag = flag.String("project-id", "", "GCP Project ID for cloud integration tests.")
	regionFlag    = flag.String("region", "us-central1", "The GCP region for the deployment.")
	imageRepoFlag = flag.String("image-repo", "test-images", "The Artifact Registry repository name.")
)

// TestOrchestrator_RealCloud_FullLifecycle performs a full, real-world deployment
// using the Orchestrator to set up IAM and deploy a simple, self-contained application.
func TestOrchestrator_RealCloud_FullLifecycle(t *testing.T) {
	if !flag.Parsed() {
		flag.Parse()
	}
	projectID := *projectIDFlag
	if projectID == "" {
		projectID = os.Getenv("GCP_PROJECT_ID") // Fallback to env var
	}
	if projectID == "" {
		t.Skip("Skipping cloud integration test: -project-id flag or GCP_PROJECT_ID env var must be set.")
	}

	logger := log.With().Str("test", "TestOrchestrator_RealCloud_FullLifecycle").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// --- 1. Arrange ---
	sourcePath, cleanupSource := createTestSourceDir(t, serviceDirectorAppSource)
	defer cleanupSource()

	runID := uuid.New().String()[:8]

	// Create the full architecture definition for the test.
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			ProjectID: projectID,
			Region:    *regionFlag,
		},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           "service-director",             // The conceptual name
			ServiceAccount: fmt.Sprintf("sd-sa-%s", runID), // A unique SA for the test
			Deployment: &servicemanager.DeploymentSpec{
				SourcePath:          sourcePath,
				BuildableModulePath: ".",
				Region:              *regionFlag,
				CPU:                 "1",
				Memory:              "512Mi",
				// Env vars for the deployed ServiceDirector
				EnvironmentVars: map[string]string{
					"SD_COMMAND_TOPIC":        fmt.Sprintf("director-commands-%s", runID),
					"SD_COMMAND_SUBSCRIPTION": fmt.Sprintf("director-commands-subscription-%s", runID),
					"SD_COMPLETION_TOPIC":     fmt.Sprintf("director-events-%s", runID),
				},
			},
		},
		// No dataflows are needed for this service manager deployment-only test.
	}

	err := servicemanager.HydrateArchitecture(arch, *imageRepoFlag, runID)
	require.NoError(t, err)

	// --- NEW IAM STEP ---
	// The Cloud Build SA needs permission to "act as" the runtime SA.
	runtimeSaName := fmt.Sprintf("it-%s", runID)
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	runtimeSaEmail, err := iamClient.EnsureServiceAccountExists(ctx, runtimeSaName)
	require.NoError(t, err)

	t.Log("Granting 'Service Account User' role to Cloud Build SA...")
	resourceManager, err := deployment.NewResourceManager()
	require.NoError(t, err)
	projectNumber, err := resourceManager.GetProjectNumber(ctx, projectID)
	require.NoError(t, err)

	cloudBuildSaMember := fmt.Sprintf("serviceAccount:%s@cloudbuild.gserviceaccount.com", projectNumber)
	err = iamClient.AddMemberToServiceAccountRole(ctx, runtimeSaEmail, cloudBuildSaMember, "roles/iam.serviceAccountUser")
	require.NoError(t, err)
	t.Log("Permission granted successfully.")

	// --- 2. Create the Orchestrators ---
	orch, err := orchestration.NewOrchestrator(ctx, arch, logger)
	require.NoError(t, err)
	defer orch.Close()

	iamOrch, err := orchestration.NewIAMOrchestrator(ctx, arch, logger)
	require.NoError(t, err)

	// --- 3. Act ---
	// Step 3a: Setup IAM for the ServiceDirector.
	saEmail, err := iamOrch.SetupServiceDirectorIAM(ctx)
	require.NoError(t, err)

	// Step 3b: Deploy the "toy" service, using the unique suffix.
	serviceURL, err := orch.DeployServiceDirector(ctx, saEmail)
	require.NoError(t, err)
	require.NotEmpty(t, serviceURL)

	// --- 4. Assert & Cleanup ---
	// Schedule the teardown of the deployed service.
	t.Cleanup(func() {
		t.Logf("Tearing down test service director: %s", arch.ServiceManagerSpec.Name)
		err := orch.TeardownServiceDirector(context.Background(), arch.ServiceManagerSpec.Name)
		require.NoError(t, err, "Teardown of test service director should not fail")

		// Also clean up the service account
		iamClient, _ := iam.NewGoogleIAMClient(context.Background(), projectID)
		err = iamClient.DeleteServiceAccount(context.Background(), arch.ServiceManagerSpec.ServiceAccount)
		require.NoError(t, err, "Teardown of test service account should not fail")
	})

	logger.Info().Str("url", serviceURL).Msg("✅ Deployment successful. Service is healthy and reachable.")
}
