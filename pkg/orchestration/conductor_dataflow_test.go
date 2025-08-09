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
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

var expectedMessages = flag.Int("expected-messages", 2, "Number of messages the verifier should expect to receive.")
var usePool = flag.Bool("use-pool", true, "Use a pool of service accounts for testing to avoid quota issues.")

// buildTestArchitecture now returns the nameMap along with the hydrated architecture.
func buildTestArchitecture(t *testing.T, projectID, region, imageRepo, runID string) (*servicemanager.MicroserviceArchitecture, map[string]string) {
	t.Helper()

	sourcePath, err := filepath.Abs("./testdata")
	require.NoError(t, err)

	// CORRECTED: Use variables for all names to avoid hardcoding and duplication.
	sdName := "sd"
	sdServiceAccount := "sa-sd"
	pubName := "tracer-publisher"
	pubServiceAccount := "sa-pub"
	subName := "tracer-subscriber"
	subServiceAccount := "sa-sub"
	commandTopicID := "director-commands"
	commandSubID := "director-command-sub"
	completionTopicID := "director-events"
	tracerTopicName := "tracer-topic"
	tracerSubName := "tracer-sub"
	validationTopic := "verify-topic"
	// we're using a 'toy' service director for the test so we only setup specific resources, not Bigquery etc...
	//bqDatasetName := "test-bq-dataset"
	//bqTableName := "test-bq-table"

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Region: region},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           sdName,
			ServiceAccount: sdServiceAccount,
			Deployment: &servicemanager.DeploymentSpec{
				SourcePath:          sourcePath,
				BuildableModulePath: "toy-servicedirector",
				EnvironmentVars: map[string]string{
					"SD_COMMAND_TOPIC":        commandTopicID,
					"SD_COMMAND_SUBSCRIPTION": commandSubID,
					"SD_COMPLETION_TOPIC":     completionTopicID,

					// GEMINI DO NOT DELETE THESE AGAIN - my comments have been very clear
					// - the service director would not usually need these but the one we deploy here is just a 'toy' version which needs them
					"TRACER_TOPIC_ID": tracerTopicName,
					"TRACER_SUB_ID":   tracerSubName,
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"tracer-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					subName: {Name: subName, ServiceAccount: subServiceAccount, Deployment: &servicemanager.DeploymentSpec{SourcePath: sourcePath, BuildableModulePath: "tracer-subscriber"}},
					pubName: {Name: pubName, ServiceAccount: pubServiceAccount, Deployment: &servicemanager.DeploymentSpec{SourcePath: sourcePath, BuildableModulePath: "tracer-publisher"}},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: tracerTopicName}, ProducerService: &servicemanager.ServiceMapping{Name: pubName, Env: "TOPIC_ID"}},
						{CloudResource: servicemanager.CloudResource{Name: validationTopic}, ProducerService: &servicemanager.ServiceMapping{Name: subName, Env: "VERIFY_TOPIC_ID"}},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{{
						CloudResource:   servicemanager.CloudResource{Name: tracerSubName},
						Topic:           tracerTopicName,
						ConsumerService: &servicemanager.ServiceMapping{Name: subName, Env: "SUBSCRIPTION_ID"},
					}},
					//BigQueryDatasets: []servicemanager.BigQueryDataset{
					//	{CloudResource: servicemanager.CloudResource{Name: bqDatasetName}},
					//},
					//BigQueryTables: []servicemanager.BigQueryTable{
					//	{
					//		CloudResource: servicemanager.CloudResource{Name: bqTableName},
					//		Dataset:       bqDatasetName,
					//		Consumers:     []servicemanager.ServiceMapping{{Name: subName, Env: "TABLE_ID"}},
					//	},
					//},
				},
			},
		},
	}
	nameMap, err := servicemanager.HydrateTestArchitecture(arch, imageRepo, runID, log.Logger)
	require.NoError(t, err)
	return arch, nameMap
}

// TestConductor_DataflowE2E_FullRun tests the Conductor's ability to execute a full,
// end-to-end deployment workflow from start to finish.
func TestConductor_DataflowE2E_FullRun(t *testing.T) {
	projectID := auth.CheckGCPAuth(t)
	logger := log.With().Str("test", "TestConductor_DataflowE2E_FullRun").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if *usePool {
		t.Setenv("TEST_SA_POOL_MODE", "true")
		t.Setenv("TEST_SA_POOL_PREFIX", "it-full-")
	}

	runID := uuid.New().String()[:8]
	arch, nameMap := buildTestArchitecture(t, projectID, "us-central1", "test-images", runID)

	hydratedVerifyTopicName := nameMap["verify-topic"]
	require.NotEmpty(t, hydratedVerifyTopicName)

	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	_, verifySub := createVerificationResources(t, ctx, psClient, hydratedVerifyTopicName)
	validationChan := startVerificationListener(t, ctx, verifySub, *expectedMessages)

	conductorOptions := orchestration.ConductorOptions{
		CheckPrerequisites:      true,
		SetupServiceDirectorIAM: true,
		DeployServiceDirector:   true,
		SetupDataflowResources:  true,
		ApplyDataflowIAM:        true,
		DeployDataflowServices:  true,
	}
	conductor, err := orchestration.NewConductor(ctx, arch, logger, conductorOptions)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conductor.Teardown(context.Background()) })

	err = conductor.Run(ctx)
	require.NoError(t, err, "Conductor.Run() should complete without errors")

	select {
	case err := <-validationChan:
		require.NoError(t, err, "Verification failed while receiving messages")
	case <-ctx.Done():
		t.Fatal("Test context timed out before verification completed.")
	}
}

// deployToyDirector is a test helper to quickly deploy a minimal Service Director.
func deployToyDirector(t *testing.T, ctx context.Context, projectID string) string {
	t.Helper()
	logger := log.With().Str("helper", "deployToyDirector").Logger()
	runID := uuid.New().String()[:8]

	sourcePath, cleanupSource := createTestSourceDir(t, serviceDirectorAppSource)
	t.Cleanup(cleanupSource)

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Region: "us-central1"},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           "toy-sd",
			ServiceAccount: "toy-sd-sa",
			Deployment:     &servicemanager.DeploymentSpec{SourcePath: sourcePath, BuildableModulePath: "."},
		},
	}
	nameMap, err := servicemanager.HydrateTestArchitecture(arch, "test-images", runID, logger)
	require.NoError(t, err)

	iamOrch, err := orchestration.NewIAMOrchestrator(ctx, arch, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamOrch.Teardown(context.Background()) })

	sourceBucket := fmt.Sprintf("%s_cloudbuild", projectID)
	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, arch.Region, sourceBucket, logger)
	require.NoError(t, err)

	saEmails, err := iamOrch.SetupServiceDirectorIAM(ctx)
	require.NoError(t, err)

	hydratedSDName := nameMap["toy-sd"]
	url, err := deployer.Deploy(ctx, hydratedSDName, saEmails[hydratedSDName], *arch.ServiceManagerSpec.Deployment)
	require.NoError(t, err)
	t.Cleanup(func() { _ = deployer.Teardown(context.Background(), hydratedSDName) })

	return url
}
