//go:build integration

package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// TestOrchestrator_DataflowE2E performs a full, end-to-end integration test of a complex dataflow.
func TestOrchestrator_DataflowE2E(t *testing.T) {
	// --- Global Test Setup ---
	projectID := auth.CheckGCPAuth(t)
	logger := log.With().Str("test", "TestOrchestrator_DataflowE2E").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	runID := uuid.New().String()[:8]

	// we always want to use a pool of service accounts for this test.
	t.Setenv("TEST_SA_POOL_MODE", "true")
	t.Setenv("TEST_SA_POOL_PREFIX", "it-")

	// --- Arrange: Define and hydrate the full architecture for the test ---
	var arch *servicemanager.MicroserviceArchitecture
	var nameMap map[string]string
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	// Define original, un-hydrated names using variables.
	sdName := "sd"
	sdServiceAccount := "sd-sa"
	sdBuildPath := "toy-servicedirector"

	dataflowName := "tracer-flow"

	pubName := "tracer-publisher"
	pubServiceAccount := "pub-sa"
	pubBuildPath := "tracer-publisher"

	subName := "tracer-subscriber"
	subServiceAccount := "sub-sa"
	subBuildPath := "tracer-subscriber"

	commandTopicID := "director-commands"
	commandSubID := "director-command-sub"
	completionTopicID := "director-events"

	verifyTopicName := "verify-topic"
	tracerTopicName := "tracer-topic"
	tracerSubName := "tracer-sub"
	//bqDatasetName := "test-bq-dataset"
	//bqTableName := "test-bq-table"
	var verifySub *pubsub.Subscription

	t.Run("Arrange - Build and Hydrate Architecture", func(t *testing.T) {
		sourcePath, _ := filepath.Abs("./testdata")
		arch = &servicemanager.MicroserviceArchitecture{
			Environment: servicemanager.Environment{ProjectID: projectID, Region: "us-central1"},
			ServiceManagerSpec: servicemanager.ServiceSpec{
				Name:           sdName,
				ServiceAccount: sdServiceAccount,
				Deployment: &servicemanager.DeploymentSpec{
					SourcePath:          sourcePath,
					BuildableModulePath: sdBuildPath,
					EnvironmentVars: map[string]string{
						"SD_COMMAND_TOPIC":        commandTopicID,
						"SD_COMMAND_SUBSCRIPTION": commandSubID,
						"SD_COMPLETION_TOPIC":     completionTopicID,
						"TRACER_TOPIC_ID":         tracerTopicName,
						"TRACER_SUB_ID":           tracerSubName,
						"VERIFY_TOPIC_ID":         verifyTopicName,
					},
				},
			},
			Dataflows: map[string]servicemanager.ResourceGroup{
				dataflowName: {
					Services: map[string]servicemanager.ServiceSpec{
						subName: {
							Name:           subName,
							ServiceAccount: subServiceAccount,
							Deployment:     &servicemanager.DeploymentSpec{SourcePath: sourcePath, BuildableModulePath: subBuildPath},
						},
						pubName: {
							Name:           pubName,
							ServiceAccount: pubServiceAccount,
							Deployment:     &servicemanager.DeploymentSpec{SourcePath: sourcePath, BuildableModulePath: pubBuildPath},
						},
					},
					Resources: servicemanager.CloudResourcesSpec{
						Topics: []servicemanager.TopicConfig{
							{
								CloudResource:   servicemanager.CloudResource{Name: tracerTopicName},
								ProducerService: &servicemanager.ServiceMapping{Name: pubName, Env: "TOPIC_ID"},
							},
							{
								CloudResource:   servicemanager.CloudResource{Name: verifyTopicName},
								ProducerService: &servicemanager.ServiceMapping{Name: subName, Env: "VERIFY_TOPIC_ID"},
							},
						},
						Subscriptions: []servicemanager.SubscriptionConfig{{
							CloudResource:   servicemanager.CloudResource{Name: tracerSubName},
							Topic:           tracerTopicName,
							ConsumerService: &servicemanager.ServiceMapping{Name: subName, Env: "SUBSCRIPTION_ID"},
						}},
						//BigQueryDatasets: []servicemanager.BigQueryDataset{{
						//	CloudResource: servicemanager.CloudResource{Name: bqDatasetName},
						//}},
						//BigQueryTables: []servicemanager.BigQueryTable{{
						//	CloudResource: servicemanager.CloudResource{Name: bqTableName},
						//	Dataset:       bqDatasetName,
						//	Consumers:     []servicemanager.ServiceMapping{{Name: subName, Env: "TABLE_ID"}},
						//	Producers:     []servicemanager.ServiceMapping{{Name: pubName, Env: "TABLE_ID"}},
						//}},
					},
				},
			},
		}
		nameMap, err = servicemanager.HydrateTestArchitecture(arch, "test-images", runID, logger)
		require.NoError(t, err)
		require.NotNil(t, nameMap)
	})

	t.Run("Act and Assert Full Workflow", func(t *testing.T) {
		iamOrch, err := orchestration.NewIAMOrchestrator(ctx, arch, logger)
		require.NoError(t, err)
		orch, err := orchestration.NewOrchestrator(ctx, arch, logger)
		require.NoError(t, err)

		t.Cleanup(func() {
			cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cCancel()
			logger.Info().Msg("--- Starting Full Teardown ---")
			if err = orch.TeardownDataflowServices(cCtx, dataflowName); err != nil {
				logger.Error().Err(err).Msg("Dataflow services teardown failed")
			}
			if err = orch.TeardownCloudRunService(cCtx, arch.ServiceManagerSpec.Name); err != nil {
				logger.Error().Err(err).Msg("Service director teardown failed")
			}
			if err = orch.Teardown(cCtx); err != nil {
				logger.Error().Err(err).Msg("Orchestrator teardown failed")
			}
			if err = iamOrch.Teardown(cCtx); err != nil {
				logger.Error().Err(err).Msg("IAM teardown failed")
			}
			logger.Info().Msg("--- Teardown Complete ---")
		})

		sdSaEmails, err := iamOrch.SetupServiceDirectorIAM(ctx)
		require.NoError(t, err)
		directorURL, err := orch.DeployServiceDirector(ctx, sdSaEmails)
		require.NoError(t, err)

		err = orch.AwaitServiceReady(ctx, nameMap[sdName])
		require.NoError(t, err, "Did not receive 'service_ready' event in time")

		err = orch.TriggerDataflowResourceCreation(ctx, dataflowName)
		require.NoError(t, err)
		err = orch.AwaitDataflowReady(ctx, dataflowName)
		require.NoError(t, err)
		dfSaEmails, err := iamOrch.ApplyIAMForDataflow(ctx, dataflowName)
		require.NoError(t, err)
		err = iamOrch.VerifyIAMForDataflow(ctx, dataflowName)
		require.NoError(t, err)

		hydratedVerifyTopicName := nameMap[verifyTopicName]
		require.NotEmpty(t, hydratedVerifyTopicName, "Could not find hydrated verification topic name in nameMap")

		_, verifySub = createVerificationResources(t, ctx, psClient, hydratedVerifyTopicName)
		validationChan := startVerificationListener(t, ctx, verifySub, 2)

		err = orch.DeployDataflowServices(ctx, dataflowName, dfSaEmails, directorURL)
		require.NoError(t, err)

		select {
		case err := <-validationChan:
			logger.Info().Err(err).Msg("Verification complete.")
		case <-ctx.Done():
			t.Fatal("Test context timed out before verification completed.")
		}
	})
}

func startVerificationListener(t *testing.T, ctx context.Context, sub *pubsub.Subscription, expectedCount int) <-chan error {
	t.Helper()
	validationChan := make(chan error, 1)
	var receivedCount atomic.Int32
	// REFACTOR: Use sync.Once to ensure the channel is closed only once.
	// This prevents a race condition where multiple concurrent callbacks could
	// attempt to close the channel, causing a panic.
	var closeOnce sync.Once

	go func() {
		err := sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
			log.Info().Str("data", string(m.Data)).Msg("Verifier received a message.")
			m.Ack()
			if receivedCount.Add(1) == int32(expectedCount) {
				// REFACTOR: Safely close the channel exactly once.
				closeOnce.Do(func() {
					close(validationChan)
				})
			}
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case validationChan <- fmt.Errorf("pubsub receive failed unexpectedly: %w", err):
			default:
			}
		}
	}()
	return validationChan
}
