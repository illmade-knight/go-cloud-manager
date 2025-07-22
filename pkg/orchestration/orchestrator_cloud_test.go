//go:build cloud_integration

package orchestration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// TestOrchestrator_RealCloud_FullLifecycle performs a full, real-world deployment
// using the Orchestrator to set up IAM and deploy a simple, self-contained application.
func TestOrchestrator_RealCloud_FullLifecycle(t *testing.T) {
	projectID := CheckGCPAuth(t)
	require.NotEmpty(t, projectID, "GCP_PROJECT_ID must be set")

	logger := log.With().Str("test", "TestOrchestrator_RealCloud_FullLifecycle").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if *usePool {
		logger.Info().Msg("✅ Test running in POOLED mode.")
		t.Setenv("TEST_SA_POOL_MODE", "true")
		t.Setenv("TEST_SA_POOL_PREFIX", "it-")
		t.Setenv("TEST_SA_POOL_NO_CREATE", "true")
	} else {
		logger.Info().Msg("Running in STANDARD mode.")
	}

	// --- 1. Arrange ---
	sourcePath, cleanupSource := createTestSourceDir(t, toyAppSource)
	defer cleanupSource()

	runID := uuid.New().String()[:8]
	serviceName := fmt.Sprintf("toy-app-%s", runID)

	arch := &servicemanager.MicroserviceArchitecture{
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

	// The hydration function will now validate the architecture and fill in defaults.
	err := servicemanager.HydrateArchitecture(arch, "test-images", "")
	require.NoError(t, err)

	// --- 2. Create the Orchestrators ---
	iamOrch, err := orchestration.NewIAMOrchestrator(ctx, arch, logger)
	require.NoError(t, err)
	orch, err := orchestration.NewOrchestrator(ctx, arch, logger)
	require.NoError(t, err)

	// --- 3. Setup Unified Teardown ---
	t.Cleanup(func() {
		t.Logf("--- Starting Full Cleanup for %s ---", arch.ServiceManagerSpec.Name)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()

		// The orchestrator's teardown will handle the service deletion.
		if err := orch.Teardown(cleanupCtx); err != nil {
			t.Errorf("Orchestrator teardown failed: %v", err)
		}
		// The IAM orchestrator's teardown will handle SA cleanup (real or fake).
		if err := iamOrch.Teardown(cleanupCtx); err != nil {
			t.Errorf("IAM teardown failed: %v", err)
		}
	})

	// --- 4. Act ---
	// Step 4a: Setup IAM for the service.
	saEmails, err := iamOrch.SetupServiceDirectorIAM(ctx)
	require.NoError(t, err)

	// Step 4b: Deploy the service.
	serviceURL, err := orch.DeployServiceDirector(ctx, saEmails)
	require.NoError(t, err)
	require.NotEmpty(t, serviceURL)

	// Step 4c: Await for the service to become healthy.
	err = orch.AwaitRevisionReady(ctx, arch.ServiceManagerSpec)
	require.NoError(t, err)

	// --- 5. Assert ---
	logger.Info().Str("url", serviceURL).Msg("✅ Deployment successful. Service is healthy and reachable.")
}
