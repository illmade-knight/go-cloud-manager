//go:build integration

package orchestration_test

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

var expectedCMessages = flag.Int("expected-messages", 2, "Number of messages the verifier should expect to receive.")
var useCPool = flag.Bool("use-pool", false, "Use a pool of service accounts for testing to avoid quota issues.")

// buildTestArchitecture is a helper to construct the complex architecture needed for the E2E tests.
func buildTestArchitecture(t *testing.T, projectID, region, imageRepo, runID string) *servicemanager.MicroserviceArchitecture {
	t.Helper()

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
					"SD_COMMAND_TOPIC":        commandTopicID,
					"SD_COMMAND_SUBSCRIPTION": commandSubID,
					"SD_COMPLETION_TOPIC":     completionTopicID,
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"tracer-flow": {
				Services: map[string]servicemanager.ServiceSpec{
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
					pubName: {
						Name:           pubName,
						ServiceAccount: fmt.Sprintf("pub-sa-%s", runID),
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          pubSourcePath,
							BuildableModulePath: ".",
							EnvironmentVars:     map[string]string{"TOPIC_ID": tracerTopicName},
						},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: tracerTopicName}},
						{CloudResource: servicemanager.CloudResource{Name: verificationTopicName}},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{
						{CloudResource: servicemanager.CloudResource{Name: tracerSubName},
							Topic:              tracerTopicName,
							AckDeadlineSeconds: 180,
						}},
				},
			},
		},
	}
	require.NoError(t, servicemanager.HydrateArchitecture(arch, imageRepo, runID, log.Logger))
	return arch
}

// TestConductor_DataflowE2E_FullRun tests the Conductor's ability to execute a full,
// end-to-end deployment workflow from start to finish.
func TestConductor_DataflowE2E_FullRun(t *testing.T) {
	projectID := auth.CheckGCPAuth(t)
	logger := log.With().Str("test", "TestConductor_DataflowE2E_FullRun").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if *useCPool {
		t.Setenv("TEST_SA_POOL_MODE", "true")
		t.Setenv("TEST_SA_POOL_PREFIX", "it-")
	}

	// --- Arrange ---
	runID := uuid.New().String()[:8]
	arch := buildTestArchitecture(t, projectID, "us-central1", "test-images", runID)
	verificationTopicName := fmt.Sprintf("verify-topic-%s", runID)

	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	_, verifySub := createVerificationResources(t, ctx, psClient, verificationTopicName)
	validationChan := startVerificationListener(t, ctx, verifySub, *expectedCMessages)

	// --- Act ---
	// Define options for a full run.
	conductorOptions := orchestration.ConductorOptions{
		SetupServiceDirectorIAM: true,
		DeployServiceDirector:   true,
		SetupDataflowResources:  true,
		ApplyDataflowIAM:        true,
		DeployDataflowServices:  true,
	}
	conductor, err := orchestration.NewConductor(ctx, arch, logger, conductorOptions)
	require.NoError(t, err)

	t.Cleanup(func() {
		cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cCancel()
		logger.Info().Msg("--- Starting Conductor Teardown ---")
		if err := conductor.Teardown(cCtx); err != nil {
			logger.Error().Err(err).Msg("Conductor teardown failed")
		}
	})

	err = conductor.Run(ctx)
	require.NoError(t, err, "Conductor.Run() should complete without errors")

	// --- Assert ---
	t.Logf("Waiting to receive %d verification message(s)...", *expectedCMessages)
	select {
	case err := <-validationChan:
		require.NoError(t, err, "Verification failed while receiving messages")
		t.Logf("✅ Verification successful! Received %d message(s).", *expectedCMessages)
	case <-ctx.Done():
		t.Fatal("Test context timed out before verification completed.")
	}
}

// TestConductor_DataflowE2E_WithSkippedDirector validates the Conductor's flexibility.
// It first runs a full deployment, then runs a second time skipping the director deployment
// and providing its URL as an override, ensuring the dataflow services can still be deployed.
func TestConductor_DataflowE2E_WithSkippedDirector(t *testing.T) {
	projectID := auth.CheckGCPAuth(t)
	logger := log.With().Str("test", "TestConductor_DataflowE2E_WithSkippedDirector").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	// --- Arrange: First full run to get a deployed director ---
	runID1 := uuid.New().String()[:8]
	arch1 := buildTestArchitecture(t, projectID, "us-central1", "test-images", runID1)

	fullRunOptions := orchestration.ConductorOptions{
		SetupServiceDirectorIAM: true,
		DeployServiceDirector:   true,
		SetupDataflowResources:  true,
		ApplyDataflowIAM:        true,
		DeployDataflowServices:  true,
	}
	conductor1, err := orchestration.NewConductor(ctx, arch1, logger, fullRunOptions)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conductor1.Teardown(context.Background()) })

	err = conductor1.Run(ctx)
	require.NoError(t, err)

	// The URL of the now-deployed director is in the hydrated architecture spec.
	directorURL := arch1.ServiceManagerSpec.Deployment.ServiceURL
	require.NotEmpty(t, directorURL, "Director URL should not be empty after first run")
	t.Logf("First run complete. Deployed director URL: %s", directorURL)

	// --- Act: Second run, skipping director deployment ---
	runID2 := uuid.New().String()[:8]
	arch2 := buildTestArchitecture(t, projectID, "us-central1", "test-images", runID2)
	verificationTopicName2 := fmt.Sprintf("verify-topic-%s", runID2)

	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })
	_, verifySub2 := createVerificationResources(t, ctx, psClient, verificationTopicName2)
	validationChan2 := startVerificationListener(t, ctx, verifySub2, *expectedCMessages)

	// These options skip the first two phases and provide the override.
	partialRunOptions := orchestration.ConductorOptions{
		SetupServiceDirectorIAM: false,
		DeployServiceDirector:   false,
		DirectorURLOverride:     directorURL,
		SetupDataflowResources:  true,
		ApplyDataflowIAM:        true,
		DeployDataflowServices:  true,
	}
	conductor2, err := orchestration.NewConductor(ctx, arch2, logger, partialRunOptions)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conductor2.Teardown(context.Background()) })

	err = conductor2.Run(ctx)
	require.NoError(t, err)

	// --- Assert ---
	t.Logf("Waiting for verification messages on second run...")
	select {
	case err := <-validationChan2:
		require.NoError(t, err, "Verification failed on second run")
		t.Logf("✅ Verification successful on second run!")
	case <-ctx.Done():
		t.Fatal("Test context timed out before verification completed on second run.")
	}
}
