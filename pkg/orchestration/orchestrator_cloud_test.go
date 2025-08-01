//go:build cloud_integration

package orchestration_test

import (
	"context"
	"flag"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// Define a flag to enable the service account pool for testing.
var usePooling = flag.Bool("use-pool", false, "Use a pool of service accounts for testing to avoid quota issues.")

// TestOrchestrator_RealCloud_FullLifecycle performs a full, real-world deployment
// using the Orchestrator to set up IAM and deploy a simple, self-contained application.
func TestOrchestrator_RealCloud_FullLifecycle(t *testing.T) {
	projectID := auth.CheckGCPAuth(t)
	require.NotEmpty(t, projectID, "GCP_PROJECT_ID must be set")

	logger := log.With().Str("test", "TestOrchestrator_RealCloud_FullLifecycle").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if *usePooling {
		logger.Info().Msg("✅ Test running in POOLED mode.")
		t.Setenv("TEST_SA_POOL_MODE", "true")
		t.Setenv("TEST_SA_POOL_PREFIX", "it-")
	}

	// --- Arrange ---
	var arch *servicemanager.MicroserviceArchitecture
	var iamOrch *orchestration.IAMOrchestrator
	var orch *orchestration.Orchestrator

	t.Run("Arrange - Build Architecture and Orchestrators", func(t *testing.T) {
		sourcePath, cleanupSource := createTestSourceDir(t, toyAppSource)
		t.Cleanup(cleanupSource)

		runID := uuid.New().String()[:8]
		serviceName := fmt.Sprintf("toy-app-%s", runID)

		arch = &servicemanager.MicroserviceArchitecture{
			Environment: servicemanager.Environment{
				ProjectID: projectID,
				Region:    "us-central1",
			},
			ServiceManagerSpec: servicemanager.ServiceSpec{
				Name:           serviceName,
				ServiceAccount: fmt.Sprintf("toy-sa-%s", runID),
				Deployment: &servicemanager.DeploymentSpec{
					SourcePath:          sourcePath,
					BuildableModulePath: ".",
				},
			},
		}

		err := servicemanager.HydrateArchitecture(arch, "test-images", "", logger)
		require.NoError(t, err)

		iamOrch, err = orchestration.NewIAMOrchestrator(ctx, arch, logger)
		require.NoError(t, err)
		orch, err = orchestration.NewOrchestrator(ctx, arch, logger)
		require.NoError(t, err)

		// Setup Unified Teardown
		t.Cleanup(func() {
			t.Logf("--- Starting Full Cleanup for %s ---", arch.ServiceManagerSpec.Name)
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cleanupCancel()

			if err := orch.Teardown(cleanupCtx); err != nil {
				t.Errorf("Orchestrator teardown failed: %v", err)
			}
			if err := iamOrch.Teardown(cleanupCtx); err != nil {
				t.Errorf("IAM teardown failed: %v", err)
			}
		})
	})

	// --- Act & Assert ---
	t.Run("Act and Assert Deployment", func(t *testing.T) {
		// Step 1: Setup IAM for the service.
		saEmails, err := iamOrch.SetupServiceDirectorIAM(ctx)
		require.NoError(t, err)

		// Step 2: Deploy the service.
		serviceURL, err := orch.DeployServiceDirector(ctx, saEmails)
		require.NoError(t, err)
		require.NotEmpty(t, serviceURL)

		// Step 3: Await for the service to become healthy.
		err = orch.AwaitRevisionReady(ctx, arch.ServiceManagerSpec)
		require.NoError(t, err)

		logger.Info().Str("url", serviceURL).Msg("✅ Deployment successful. Service is healthy and reachable.")
	})
}
