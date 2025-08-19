//go:build integration

package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// In orchestrator_dataflow_test.go

// preflightServiceConfigs generates the service-specific YAML files and then
// runs a validation command (e.g., `go test`) in each service's source
// directory to ensure the generated config is valid and readable.
func preflightServiceConfigs(t *testing.T, arch *servicemanager.MicroserviceArchitecture) {
	t.Helper()

	// 2. Iterate through each service
	t.Log("Running local preflight validation for each service...")
	for _, df := range arch.Dataflows {
		for _, service := range df.Services {
			if service.Deployment == nil {
				continue
			}
			serviceDir := filepath.Join(service.Deployment.SourcePath, service.Deployment.BuildableModulePath)

			t.Logf("Validating service in directory: %s", serviceDir)

			// Here we assume `go test` in the service's directory is sufficient
			// to validate its ability to load the config.
			cmd := exec.Command("go", "test", "./...")
			cmd.Dir = serviceDir
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "Preflight validation failed for service '%s'. Output:\n%s", service.Name, string(output))
		}
	}
	t.Log("✅ All services passed local preflight validation.")
}

// TestConductor_Dataflow_CloudIntegration is the final end-to-end cloud integration test for the Conductor.
//
// Its primary purpose is to verify the Conductor's ability to orchestrate a complex,
// parallel build-and-deploy workflow against real cloud services.
//
// This test uses a self-contained Go module located in the `testdata` directory. This module
// requires a stable, versioned release of the main repository from GitHub. This strategy
// ensures that the build process is isolated and reproducible.
func TestConductor_Dataflow_CloudIntegration(t *testing.T) {
	// --- Global Test Setup ---
	projectID := auth.CheckGCPAuth(t)
	logger := log.With().Str("test", "TestConductor_Dataflow_CloudIntegration").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	runID := uuid.New().String()[:8]
	t.Setenv("TEST_SA_POOL_MODE", "true")
	t.Setenv("TEST_SA_POOL_PREFIX", "it-")

	// --- Arrange ---
	var arch *servicemanager.MicroserviceArchitecture
	var nameMap map[string]string
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	// The sourcePath for the build is the self-contained testdata module.
	sourcePath, err := filepath.Abs("./testdata")
	require.NoError(t, err)

	// Build paths are now relative to the testdata directory.
	sdBuildPath := "servicedirector"
	pubBuildPath := "tracer-publisher"
	subBuildPath := "tracer-subscriber"

	sdName, sdServiceAccount := "sd", "sd-sa"
	dataflowName := "tracer-flow"
	commandFlowName := "command-flow"
	pubName, pubServiceAccount := "tracer-publisher", "pub-sa"
	subName, subServiceAccount := "tracer-subscriber", "sub-sa"
	verifyTopicName, tracerTopicName, tracerSubName := "verify-topic", "tracer-topic", "tracer-sub"
	sdCommandTopicName, sdCompletionTopicName, sdCommandSubName := "director-commands", "director-events", "director-command-sub"

	// 1. Define Architecture with logical names.
	arch = &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Region: "us-central1"},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           sdName,
			ServiceAccount: sdServiceAccount,
			Deployment: &servicemanager.DeploymentSpec{
				SourcePath:          sourcePath,
				BuildableModulePath: sdBuildPath,
				EnvironmentVars: map[string]string{
					"SD_COMMAND_TOPIC":        sdCommandTopicName,
					"SD_COMMAND_SUBSCRIPTION": sdCommandSubName,
					"SD_COMPLETION_TOPIC":     sdCompletionTopicName,
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			commandFlowName: {
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource: servicemanager.CloudResource{
								Name: sdCommandTopicName,
							},
							ProducerService: &servicemanager.ServiceMapping{
								Name: sdName,
								Lookup: servicemanager.Lookup{
									Key:    "command-topic-id",
									Method: servicemanager.LookupYAML,
								},
							},
						},
						{
							CloudResource: servicemanager.CloudResource{
								Name: sdCompletionTopicName,
							},
							ProducerService: &servicemanager.ServiceMapping{
								Name: sdName,
								Lookup: servicemanager.Lookup{
									Key:    "completion-topic-id",
									Method: servicemanager.LookupYAML,
								},
							},
						},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{
						{
							CloudResource: servicemanager.CloudResource{
								Name: sdCommandSubName,
							},
							Topic: sdCommandTopicName,
							ConsumerService: &servicemanager.ServiceMapping{
								Name: sdName,
								Lookup: servicemanager.Lookup{
									Key:    "command-subscription-id",
									Method: servicemanager.LookupYAML,
								},
							},
						},
					},
				},
			},
			dataflowName: {
				Services: map[string]servicemanager.ServiceSpec{
					subName: {
						Name:           subName,
						ServiceAccount: subServiceAccount,
						Dependencies:   []string{sdName},
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          sourcePath,
							BuildableModulePath: subBuildPath,
						},
					},
					pubName: {
						Name:           pubName,
						ServiceAccount: pubServiceAccount,
						Dependencies:   []string{sdName},
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          sourcePath,
							BuildableModulePath: pubBuildPath,
						},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource: servicemanager.CloudResource{
								Name: tracerTopicName,
							},
							ProducerService: &servicemanager.ServiceMapping{
								Name: pubName,
								Lookup: servicemanager.Lookup{
									Key:    "TRACER_TOPIC_ID",
									Method: servicemanager.LookupYAML,
								},
							},
						},
						{
							CloudResource: servicemanager.CloudResource{
								Name: verifyTopicName,
							},
							ProducerService: &servicemanager.ServiceMapping{
								Name: subName,
								Lookup: servicemanager.Lookup{
									Key:    "VERIFY_TOPIC_ID",
									Method: servicemanager.LookupYAML,
								},
							},
						},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{{
						CloudResource: servicemanager.CloudResource{
							Name: tracerSubName,
						},
						Topic: tracerTopicName,
						ConsumerService: &servicemanager.ServiceMapping{
							Name: subName,
							Lookup: servicemanager.Lookup{
								Key:    "TRACER_SUB_ID",
								Method: servicemanager.LookupYAML,
							},
						},
					}},
				},
			},
		},
	}

	// 2. Hydrate the architecture with unique resource names and pooled SA emails.
	iamClient, err := iam.NewTestIAMClient(ctx, projectID, logger, "it-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	//
	// This performs the same role as PrepareServiceDirectorSource in a production deployment
	//
	nameMap, err = servicemanager.HydrateTestArchitecture(arch, "test-images", runID, logger)
	require.NoError(t, err)

	// advanced preflight validation
	t.Log("Generating service-specific YAML configurations...")
	sc, err := orchestration.GenerateServiceConfigs(arch)
	require.NoError(t, err)

	err = orchestration.WriteServiceConfigFiles(sc, logger)
	require.NoError(t, err)

	// 3. NEW: Run the local preflight check before any cloud deployment.
	preflightServiceConfigs(t, arch)

	leasedSDEmail, err := iamClient.EnsureServiceAccountExists(ctx, arch.ServiceManagerSpec.ServiceAccount)
	require.NoError(t, err)
	arch.ServiceManagerSpec.ServiceAccount = leasedSDEmail
	for dfName, df := range arch.Dataflows {
		for svcName, svc := range df.Services {
			leasedEmail, err := iamClient.EnsureServiceAccountExists(ctx, svc.ServiceAccount)
			require.NoError(t, err)
			svc.ServiceAccount = leasedEmail
			df.Services[svcName] = svc
		}
		arch.Dataflows[dfName] = df
	}

	arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMMAND_TOPIC"] = nameMap[sdCommandTopicName]
	arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMPLETION_TOPIC"] = nameMap[sdCompletionTopicName]
	arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMMAND_SUBSCRIPTION"] = nameMap[sdCommandSubName]

	// 3. Write the fully hydrated YAML for the ServiceDirector build.
	t.Log("Writing hydrated services.yaml for the ServiceDirector build...")
	yamlBytes, err := yaml.Marshal(arch)
	require.NoError(t, err)
	embeddedYamlPath := filepath.Join(sourcePath, sdBuildPath, "services.yaml")
	err = os.WriteFile(embeddedYamlPath, yamlBytes, 0644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(embeddedYamlPath) })

	//
	// OK we've finished replicating the PrepareServiceDirectorSource logic for the test environment
	//

	// 4. Pre-create test resources.
	t.Log("Pre-creating test resources...")
	hydratedVerifyTopicName := nameMap[verifyTopicName]
	hydratedTracerTopicName := nameMap[tracerTopicName]
	verifyTopic, err := psClient.CreateTopic(ctx, hydratedVerifyTopicName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = verifyTopic.Delete(context.Background()) })
	tracerTopic, err := psClient.CreateTopic(ctx, hydratedTracerTopicName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tracerTopic.Delete(context.Background()) })
	t.Log("Test resources created successfully.")

	// --- Act ---
	t.Log("Creating Conductor...")
	opts := orchestration.ConductorOptions{
		CheckPrerequisites:     true,
		SetupIAM:               true,
		BuildAndDeployServices: true,
		TriggerRemoteSetup:     true,
		VerifyDataflowIAM:      true,
		SAPollTimeout:          4 * time.Minute,
		PolicyPollTimeout:      7 * time.Minute, // Generous timeout for policy propagation
	}
	conductor, err := orchestration.NewConductor(ctx, arch, logger, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conductor.Teardown(context.Background()) })

	_, verifySub := createVerificationResources(t, ctx, psClient, hydratedVerifyTopicName)
	validationChan := startVerificationListener(t, ctx, verifySub, 2)

	t.Log("Running Conductor to execute parallel build and deploy workflow...")
	err = conductor.Run(ctx)
	require.NoError(t, err)
	t.Log("Conductor run completed successfully.")

	// --- Assert ---
	t.Log("Waiting for verification messages from deployed services...")
	select {
	case err := <-validationChan:
		require.NoError(t, err, "Verification listener received an error")
		log.Info().Msg("✅ Verification complete. All messages received.")
	case <-ctx.Done():
		t.Fatal("Test context timed out before receiving all verification messages.")
	}
}

// startVerificationListener is a test helper that listens on a subscription for a specified number of messages.
func startVerificationListener(t *testing.T, ctx context.Context, sub *pubsub.Subscription, expectedCount int) <-chan error {
	t.Helper()
	validationChan := make(chan error, 1)
	var receivedCount atomic.Int32
	var closeOnce sync.Once

	go func() {
		err := sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
			log.Info().Str("data", string(m.Data)).Msg("Verifier received a message.")
			m.Ack()
			if receivedCount.Add(1) >= int32(expectedCount) {
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

// createVerificationResources is a test helper that creates the ephemeral Pub/Sub resources needed by the test runner.
func createVerificationResources(t *testing.T, ctx context.Context, client *pubsub.Client, hydratedTopicName string) (*pubsub.Topic, *pubsub.Subscription) {
	t.Helper()
	verifyTopic := client.Topic(hydratedTopicName)
	exists, err := verifyTopic.Exists(ctx)
	require.NoError(t, err)
	require.True(t, exists)

	subID := fmt.Sprintf("verify-sub-%s", uuid.New().String()[:8])
	verifySub, err := client.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic:             verifyTopic,
		AckDeadline:       20 * time.Second,
		RetentionDuration: 10 * time.Minute,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = verifySub.Delete(context.Background())
	})

	return verifyTopic, verifySub
}
