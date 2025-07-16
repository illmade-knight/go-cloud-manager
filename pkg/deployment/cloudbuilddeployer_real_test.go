//go:build cloud_integration

package deployment_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/stretchr/testify/assert"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"cloud.google.com/go/iam/admin/apiv1"
	"cloud.google.com/go/iam/admin/apiv1/adminpb"
	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- Test-Local Helper Code ---

var sourcePathFlag = flag.String("source-path", "", "Optional. Path to a local source directory to build and deploy.")

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

func setupTestRunnerSA(t *testing.T, ctx context.Context, projectID string) string {
	t.Helper()
	if saEmail := os.Getenv("GCP_TEST_RUNNER_SA"); saEmail != "" {
		t.Logf("Using existing service account from GCP_TEST_RUNNER_SA: %s", saEmail)
		return saEmail
	}

	t.Log("GCP_TEST_RUNNER_SA not set. Creating temporary service account for this test run.")
	iamClient, err := deployment.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)

	runID := uuid.New().String()[:8]
	saName := fmt.Sprintf("temp-runner-sa-%s", runID)

	saEmail, err := iamClient.EnsureServiceAccountExists(ctx, saName)
	require.NoError(t, err)
	t.Logf("Created temporary service account: %s", saEmail)

	t.Cleanup(func() {
		t.Logf("Cleaning up temporary service account: %s", saName)
		adminClient, err := admin.NewIamClient(context.Background())
		if err != nil {
			t.Logf("Failed to create IAM admin adminClient for cleanup: %v", err)
			return
		}
		defer adminClient.Close()
		resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", projectID, saEmail)
		req := &adminpb.DeleteServiceAccountRequest{Name: resourceName}
		if err := adminClient.DeleteServiceAccount(context.Background(), req); err != nil && status.Code(err) != codes.NotFound {
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
	return tmpDir, func() { os.RemoveAll(tmpDir) }
}

// --- Main Integration Test ---

func TestCloudBuildDeployer_RealIntegration(t *testing.T) {
	if !flag.Parsed() {
		flag.Parse()
	}
	projectID := checkGCPAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	logger := zerolog.New(os.Stderr).With().Timestamp().Str("test", "CloudBuildDeployer_Real").Logger()

	// --- Arrange ---
	runID := uuid.New().String()[:8]
	serviceName := fmt.Sprintf("it-cb-deployer-svc-%s", runID)
	region := "us-central1"
	sourceBucketName := fmt.Sprintf("%s_cloudbuild", projectID)
	imageRepoName := "test-images"
	imageName := serviceName
	imageTag := fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:latest", region, projectID, imageRepoName, imageName)
	sourceObject := fmt.Sprintf("source/%s-%d.tar.gz", serviceName, time.Now().UnixNano())

	createdCloudBuildPermissions, err := deployment.DiagnoseAndFixCloudBuildPermissions(ctx, projectID, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to diagnose and fix Cloud Build Permissions")
	}

	resourceManager, err := deployment.NewResourceManager()
	require.NoError(t, err)

	//project, err := resourceManager.GetProject(ctx, projectID)

	projectNumber, err := resourceManager.GetProjectNumber(ctx, projectID)
	require.NoError(t, err)

	iamClient, err := deployment.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)

	agentPermissionCreated, err := deployment.GrantCloudRunAgentPermissions(ctx, deployment.RepoConfig{
		ProjectID:     projectID,
		ProjectNumber: projectNumber,
		Name:          imageRepoName,
		Location:      region,
	}, iamClient)
	require.NoError(t, err)

	if createdCloudBuildPermissions || agentPermissionCreated {
		logger.Info().Msg("Waiting 2 minutes for IAM permissions to propagate...")
		time.Sleep(2 * time.Minute)
		logger.Info().Msg("Wait complete. You can now run the main integration test.")
	}

	// Set source path based on flag
	var sourcePath string
	if *sourcePathFlag != "" {
		logger.Info().Str("path", *sourcePathFlag).Msg("Using provided source path from -source-path flag.")
		sourcePath = *sourcePathFlag
	} else {
		logger.Info().Msg("Using in-memory 'hello-world' source for deployment.")
		var cleanupSource func()
		sourcePath, cleanupSource = createTestSourceDir(t, helloWorldSource)
		defer cleanupSource()
	}

	// Create all necessary clients
	gcsClient, err := storage.NewClient(ctx)
	require.NoError(t, err)
	defer gcsClient.Close()
	arClient, err := artifactregistry.NewClient(ctx)
	require.NoError(t, err)
	defer arClient.Close()

	// Ensure prerequisites exist
	logger.Info().Msg("Verifying prerequisites (GCS Bucket and Artifact Registry Repo)...")
	bucket := gcsClient.Bucket(sourceBucketName)
	if _, err := bucket.Attrs(ctx); err != nil {
		if errors.Is(err, storage.ErrBucketNotExist) {
			logger.Info().Str("bucket", sourceBucketName).Msg("Cloud Build source bucket not found, creating it now...")
			require.NoError(t, bucket.Create(ctx, projectID, nil), "Failed to create GCS source bucket")
		} else {
			t.Fatalf("Failed to check for GCS source bucket %s: %v", sourceBucketName, err)
		}
	}
	require.NoError(t, deployment.EnsureArtifactRegistryRepositoryExists(ctx, projectID, region, imageRepoName, logger))
	logger.Info().Msg("Prerequisites verified successfully.")

	// Create the deployer instance
	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, region, sourceBucketName, logger)
	require.NoError(t, err)

	// Get a service account for the test run
	saEmail := setupTestRunnerSA(t, ctx, projectID)

	spec := servicemanager.DeploymentSpec{
		SourcePath: sourcePath,
		Image:      imageTag,
		CPU:        "1",
		Memory:     "512Mi",
	}

	// Setup full cleanup for all created resources
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		t.Logf("--- Starting Full Cleanup for %s ---", serviceName)
		assert.NoError(t, deployer.Teardown(cleanupCtx, serviceName), "Teardown of Cloud Run service should not fail")

		imagePackageName := fmt.Sprintf("projects/%s/locations/%s/repositories/%s/packages/%s", projectID, region, imageRepoName, imageName)
		op, err := arClient.DeletePackage(cleanupCtx, &artifactregistrypb.DeletePackageRequest{Name: imagePackageName})
		if err == nil {
			op.Wait(cleanupCtx)
		} else if status.Code(err) != codes.NotFound {
			t.Errorf("Failed to trigger Artifact Registry package deletion: %v", err)
		}

		err = gcsClient.Bucket(sourceBucketName).Object(sourceObject).Delete(cleanupCtx)
		if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
			t.Errorf("Failed to delete GCS source object: %v", err)
		}
	})

	//cloudBuildSA := fmt.Sprintf("projects/%s/serviceAccounts/885150127230@cloudbuild.gserviceaccount.com", projectID)
	cloudBuildSA := ""
	// --- Act ---
	logger.Info().Str("service", serviceName).Str("source", sourcePath).Msg("Deploying service...")
	serviceURL, err := deployer.Deploy(ctx, serviceName, cloudBuildSA, saEmail, spec)
	require.NoError(t, err)
	require.NotEmpty(t, serviceURL)
	logger.Info().Str("url", serviceURL).Msg("Service deployed successfully.")

	//
	// --- Assert ---
	logger.Info().Str("service", serviceName).Msg("Verifying service is ready by polling the latest revision...")

	regionalEndpoint := fmt.Sprintf("%s-run.googleapis.com:443", region)
	runService, err := run.NewService(ctx, option.WithEndpoint(regionalEndpoint))
	require.NoError(t, err)

	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", projectID, region, serviceName)

	// This is the final, corrected health check logic.
	require.Eventually(t, func() bool {
		// 1. Get the parent Service to find the name of the latest revision.
		svc, err := runService.Projects.Locations.Services.Get(fullServiceName).Do()
		if err != nil {
			logger.Error().Err(err).Str("service", serviceName).Msg("Failed to get service")
			return false
		}
		// The latest created revision is the one we want to check.
		latestRevisionName := svc.LatestCreatedRevision

		latestReadyRevision := svc.LatestReadyRevision

		logger.Info().Str("service", serviceName).Str("latest ready", latestReadyRevision).Str("latest create", latestRevisionName).Msg("Service is ready by polling the latest revision...")

		//fullRevisionName := fmt.Sprintf("projects/%s/locations/%s/services/%s/revisions/%s", projectID, region, serviceName, latestReadyRevision)
		// 2. Get the Revision object directly.
		rev, err := runService.Projects.Locations.Services.Revisions.Get(latestReadyRevision).Do()
		if err != nil {
			return false
		}

		// 3. Check the 'Ready' condition on the Revision itself.
		for _, cond := range rev.Conditions {
			if cond.Type == "Ready" {
				if cond.State == "CONDITION_SUCCEEDED" {
					return true // Success!
				}
				// Log the reason if it's not ready yet, which is helpful for debugging.
				logger.Info().Str("revision", rev.Name).Str("reason", cond.Reason).Str("message", cond.Message).Msg("Revision not ready yet...")
			}
		}
		return false
	}, 3*time.Minute, 10*time.Second, "Cloud Run Revision never reported a 'Ready' status of 'True'.")

	logger.Info().Msg("✅ Revision is Ready. Deployment successful.")
}
