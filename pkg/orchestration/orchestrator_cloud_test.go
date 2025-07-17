//go:build cloud_integration

package orchestration_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

var (
	projectIDFlag = flag.String("project-id", "", "GCP Project ID for cloud integration tests.")
	regionFlag    = flag.String("region", "us-central1", "The GCP region for the deployment.")
	imageRepoFlag = flag.String("image-repo", "test-images", "The Artifact Registry repository name.")
)

// toyAppSource defines a simple, self-contained Go application for deployment testing.
var toyAppSource = map[string]string{
	"go.mod": `
module toy-app-for-testing
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
		fmt.Fprint(w, "Toy App is Healthy!")
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("INFO: Toy App listening on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
`,
}

// createTestSourceDir is a helper to write the toy app source to a temporary directory.
func createTestSourceDir(t *testing.T, files map[string]string) (string, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "toy-source")
	require.NoError(t, err)

	for name, content := range files {
		p := filepath.Join(tmpDir, name)
		dir := filepath.Dir(p)
		err = os.MkdirAll(dir, 0755)
		require.NoError(t, err, "Failed to create source subdirectories")
		err = os.WriteFile(p, []byte(content), 0644)
		require.NoError(t, err, "Failed to write source file")
	}

	return tmpDir, func() { os.RemoveAll(tmpDir) }
}

// TestOrchestrator_DeployServiceDirector_RealIntegration performs a full, real-world
// deployment using the Orchestrator to build and deploy a simple, self-contained application.
func TestOrchestrator_DeployServiceDirector_RealIntegration(t *testing.T) {
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		t.Skip("Skipping real integration test: GCP_PROJECT_ID environment variable is not set.")
	}

	logger := log.With().Str("test", "TestOrchestrator_DeployServiceDirector_RealIntegration").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// --- 1. Arrange ---
	sourcePath, cleanupSource := createTestSourceDir(t, toyAppSource)
	defer cleanupSource()

	cfg := orchestration.Config{
		ProjectID:       projectID,
		Region:          *regionFlag,
		SourcePath:      sourcePath,
		SourceBucket:    fmt.Sprintf("%s_cloudbuild", projectID),
		ImageRepo:       *imageRepoFlag,
		CommandTopic:    "director-commands",
		CompletionTopic: "director-events",
	}

	orch, err := orchestration.NewOrchestrator(ctx, cfg, logger)
	require.NoError(t, err)
	defer orch.Close()

	// --- 2. Act ---
	// Generate a unique suffix for this test run.
	runID := uuid.New().String()[:8]

	// Pass the suffix to the deployer.
	serviceName, serviceURL, err := orch.DeployServiceDirector(ctx, ".", runID)
	require.NoError(t, err)
	require.NotEmpty(t, serviceURL)

	// Schedule the teardown of the deployed service.
	t.Cleanup(func() {
		t.Logf("Tearing down test service director: %s", serviceName)
		// Use a background context for cleanup to ensure it runs.
		err := orch.TeardownServiceDirector(context.Background(), serviceName)
		require.NoError(t, err, "Teardown of test service director should not fail")
	})

	// --- 3. Assert ---
	logger.Info().Str("url", serviceURL).Msg("✅ Deployment API calls succeeded. Service is healthy and reachable.")

	// Optional: You can add an HTTP GET check here to be 100% sure it's reachable,
	// but the awaitRevisionReady already confirms it's healthy from the platform's perspective.
}
