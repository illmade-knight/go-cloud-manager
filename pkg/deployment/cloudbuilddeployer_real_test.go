//go:build cloud_integration

package deployment_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/illmade-knight/go-test/auth"

	"cloud.google.com/go/iam/admin/apiv1"
	"cloud.google.com/go/iam/admin/apiv1/adminpb"
	"cloud.google.com/go/pubsub"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ensureCloudBuildPermissions uses the refactored iam package to programmatically
// grant the necessary roles to the default Cloud Build service account.
func ensureCloudBuildPermissions(t *testing.T, ctx context.Context, projectID string) {
	t.Helper()
	t.Log("Ensuring default Cloud Build service account has necessary permissions...")

	// 1. Get the project number to construct the SA email.
	rmClient, err := resourcemanager.NewProjectsClient(ctx)
	require.NoError(t, err)
	defer func() {
		_ = rmClient.Close()
	}()

	proj, err := rmClient.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{
		Name: fmt.Sprintf("projects/%s", projectID),
	})
	require.NoError(t, err)
	projectNumber := proj.Name[len("projects/"):]

	// 2. Define the required roles and the member.
	cloudBuildMember := fmt.Sprintf("serviceAccount:%s@cloudbuild.gserviceaccount.com", projectNumber)
	requiredRoles := []string{
		"roles/cloudbuild.serviceAgent",
		"roles/storage.objectViewer",
		"roles/artifactregistry.writer",
		"roles/run.admin",
		"roles/iam.serviceAccountUser",
	}

	// 3. Use the IAMProjectManager to grant the roles.
	projectManager, err := iam.NewIAMProjectManager(ctx, projectID)
	require.NoError(t, err)
	defer func() {
		_ = projectManager.Close()
	}()

	var grantedRoles bool
	for _, role := range requiredRoles {
		err = projectManager.AddProjectIAMBinding(ctx, cloudBuildMember, role)
		if err == nil {
			// This is a simplification; the real AddProjectIAMBinding is idempotent.
			// We track if any change was likely made.
			grantedRoles = true
		}
		require.NoError(t, err, "Failed to grant role %s", role)
	}

	// If we granted new roles, wait for them to propagate.
	if grantedRoles {
		t.Log("Granted new IAM roles to Cloud Build SA. Waiting 2 minutes for propagation...")
		time.Sleep(2 * time.Minute)
	} else {
		t.Log("All required Cloud Build permissions already exist.")
	}
}

// --- Test-Local Helper Code ---
var (
	sourcePathFlag = flag.String("source-path", "", "Optional. Path to a local source directory to build and deploy.")
	regionFlag     = flag.String("region", "us-central1", "The GCP region for the deployment.")
	imageRepoFlag  = flag.String("image-repo", "test-images", "The Artifact Registry repository name.")
)

var helloWorldSource = map[string]string{
	"go.mod": `
module hello-world-service
go 1.22
`,
	"main.go": `
package main
import (
	"fmt"
	"log"
	"net/http"
	"os"
)
func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Hello from Cloud Run!")
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
`,
}

// CheckGCPAuth is a helper that fails fast if the test is not configured to run.
func CheckGCPAuth(t *testing.T) string {
	t.Helper()
	projectID := auth.CheckGCPAuth(t)
	// A simple client creation is enough to check basic auth config.
	_, err := pubsub.NewClient(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GCP AUTHENTICATION FAILED! Please run 'gcloud auth application-default login'. Original Error: %v", err)
	}
	return projectID
}

func setupTestRunnerSA(t *testing.T, ctx context.Context, projectID string) string {
	t.Helper()
	if saEmail := os.Getenv("GCP_TEST_RUNNER_SA"); saEmail != "" {
		t.Logf("Using existing service account from GCP_TEST_RUNNER_SA: %s", saEmail)
		return saEmail
	}

	t.Log("GCP_TEST_RUNNER_SA not set. Creating temporary service account for this test run.")
	iamClient, err := admin.NewIamClient(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	runID := uuid.New().String()[:8]
	saName := fmt.Sprintf("temp-runner-sa-%s", runID)
	saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", saName, projectID)

	_, err = iamClient.CreateServiceAccount(ctx, &adminpb.CreateServiceAccountRequest{
		Name:      fmt.Sprintf("projects/%s", projectID),
		AccountId: saName,
	})
	require.NoError(t, err)
	t.Logf("Created temporary service account: %s", saEmail)

	t.Cleanup(func() {
		t.Logf("Cleaning up temporary service account: %s", saName)
		resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", projectID, saEmail)
		req := &adminpb.DeleteServiceAccountRequest{Name: resourceName}
		err := iamClient.DeleteServiceAccount(context.Background(), req)
		if err != nil && status.Code(err) != codes.NotFound {
			t.Logf("Failed to delete temporary service account %s: %v", saEmail, err)
		}
	})
	return saEmail
}

func createTestSourceDir(t *testing.T, files map[string]string) (string, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "source")
	require.NoError(t, err)
	for name, content := range files {
		p := filepath.Join(tmpDir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0644))
	}
	return tmpDir, func() { _ = os.RemoveAll(tmpDir) }
}

// --- Main Integration Test ---

func TestCloudBuildDeployer_RealIntegration(t *testing.T) {
	if !flag.Parsed() {
		flag.Parse()
	}
	projectID := CheckGCPAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute) // Increased timeout for IAM propagation
	defer cancel()

	logger := zerolog.New(os.Stderr).With().Timestamp().Str("test", "CloudBuildDeployer_Real").Logger()

	// --- Arrange ---
	// REFACTOR: Ensure Cloud Build permissions are set before running the test.
	ensureCloudBuildPermissions(t, ctx, projectID)

	runID := uuid.New().String()[:8]
	serviceName := fmt.Sprintf("it-cb-deployer-svc-%s", runID)
	region := *regionFlag
	imageRepoName := *imageRepoFlag
	sourceBucketName := fmt.Sprintf("%s_cloudbuild", projectID)
	imageName := serviceName
	imageTag := fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:latest", region, projectID, imageRepoName, imageName)

	// Set source path based on flag or use in-memory default.
	var sourcePath string
	if *sourcePathFlag != "" {
		logger.Info().Str("path", *sourcePathFlag).Msg("Using provided source path from -source-path flag.")
		sourcePath = *sourcePathFlag
	} else {
		logger.Info().Msg("Using in-memory 'hello-world' source for deployment.")
		var cleanupSource func()
		sourcePath, cleanupSource = createTestSourceDir(t, helloWorldSource)
		t.Cleanup(cleanupSource)
	}

	// Create the deployer instance.
	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, region, sourceBucketName, logger)
	require.NoError(t, err)

	// Get a service account for the test run.
	saEmail := setupTestRunnerSA(t, ctx, projectID)

	spec := servicemanager.DeploymentSpec{
		SourcePath:          sourcePath,
		Image:               imageTag,
		ImageRepo:           imageRepoName,
		CPU:                 "1",
		Memory:              "512Mi",
		BuildableModulePath: ".",
		Region:              region,
	}

	// Setup full cleanup for all created resources.
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		t.Logf("--- Starting Full Cleanup for %s ---", serviceName)
		assert.NoError(t, deployer.Teardown(cleanupCtx, serviceName), "Teardown of Cloud Run service should not fail")
	})

	// --- Act & Assert ---
	var serviceURL string
	t.Run("Act - Deploy Service", func(t *testing.T) {
		logger.Info().Str("service", serviceName).Str("source", sourcePath).Msg("Deploying service...")
		var deployErr error
		serviceURL, deployErr = deployer.Deploy(ctx, serviceName, saEmail, spec)
		require.NoError(t, deployErr)
		require.NotEmpty(t, serviceURL)
		logger.Info().Str("url", serviceURL).Msg("Service deployed successfully.")
	})

	t.Run("Assert - Verify Service Ready", func(t *testing.T) {
		logger.Info().Str("service", serviceName).Msg("Verifying service is ready by polling the latest revision...")

		regionalEndpoint := fmt.Sprintf("%s-run.googleapis.com:443", region)
		runService, err := run.NewService(ctx, option.WithEndpoint(regionalEndpoint))
		require.NoError(t, err)

		fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", projectID, region, serviceName)

		require.Eventually(t, func() bool {
			svc, err := runService.Projects.Locations.Services.Get(fullServiceName).Do()
			if err != nil {
				logger.Error().Err(err).Str("service", serviceName).Msg("Failed to get service")
				return false
			}
			if svc.LatestReadyRevision == "" {
				return false
			}

			rev, err := runService.Projects.Locations.Services.Revisions.Get(svc.LatestReadyRevision).Do()
			if err != nil {
				return false
			}

			for _, cond := range rev.Conditions {
				if cond.Type == "Ready" {
					if cond.State == "CONDITION_SUCCEEDED" {
						return true // Success!
					}
					logger.Info().Str("revision", rev.Name).Str("reason", cond.Reason).Str("message", cond.Message).Msg("Revision not ready yet...")
				}
			}
			return false
		}, 3*time.Minute, 10*time.Second, "Cloud Run Revision never reported a 'Ready' status of 'True'.")

		logger.Info().Msg("✅ Revision is Ready. Deployment successful.")
	})
}
