//go:build integration

package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	"gopkg.in/yaml.v3"
)

func getRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		goModPath := filepath.Join(cwd, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return cwd, nil
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return "", errors.New("could not find go.mod in any parent directory")
		}
		cwd = parent
	}
}

func TestConductor_Dataflow_CloudIntegration(t *testing.T) {
	// --- Global Test Setup ---
	projectID := auth.CheckGCPAuth(t)
	logger := log.With().Str("test", "TestConductor_Dataflow_CloudIntegration").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
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

	sourcePath, err := getRepoRoot()
	require.NoError(t, err)

	// Define build paths relative to the repo root, mirroring the production structure.
	sdBuildPath := "test/cmd/toy-servicedirector"
	pubBuildPath := "test/cmd/tracer-publisher"
	subBuildPath := "test/cmd/tracer-subscriber"

	sdName, sdServiceAccount := "sd", "sd-sa"
	dataflowName := "tracer-flow"
	pubName, pubServiceAccount := "tracer-publisher", "pub-sa"
	subName, subServiceAccount := "tracer-subscriber", "sub-sa"
	verifyTopicName, tracerTopicName, tracerSubName := "verify-topic", "tracer-topic", "tracer-sub"

	// 1. Define and Hydrate Architecture
	arch = &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Region: "us-central1"},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           sdName,
			ServiceAccount: sdServiceAccount,
			Deployment: &servicemanager.DeploymentSpec{
				SourcePath:          sourcePath,
				BuildableModulePath: sdBuildPath,
				EnvironmentVars: map[string]string{
					"SD_COMMAND_TOPIC":        "director-commands",
					"SD_COMMAND_SUBSCRIPTION": "director-command-sub",
					"SD_COMPLETION_TOPIC":     "director-events",
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			dataflowName: {
				Services: map[string]servicemanager.ServiceSpec{
					subName: {Name: subName, ServiceAccount: subServiceAccount, Deployment: &servicemanager.DeploymentSpec{SourcePath: sourcePath, BuildableModulePath: subBuildPath}},
					pubName: {Name: pubName, ServiceAccount: pubServiceAccount, Deployment: &servicemanager.DeploymentSpec{SourcePath: sourcePath, BuildableModulePath: pubBuildPath}},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: tracerTopicName}, ProducerService: &servicemanager.ServiceMapping{Name: pubName, Env: "TOPIC_ID"}},
						{CloudResource: servicemanager.CloudResource{Name: verifyTopicName}, ProducerService: &servicemanager.ServiceMapping{Name: subName, Env: "VERIFY_TOPIC_ID"}},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{{
						CloudResource:   servicemanager.CloudResource{Name: tracerSubName},
						Topic:           tracerTopicName,
						ConsumerService: &servicemanager.ServiceMapping{Name: subName, Env: "SUBSCRIPTION_ID"},
					}},
				},
			},
		},
	}
	nameMap, err = servicemanager.HydrateTestArchitecture(arch, "test-images", runID, logger)
	require.NoError(t, err)

	t.Log("Writing hydrated services.yaml for the ServiceDirector build...")
	yamlBytes, err := yaml.Marshal(arch)
	require.NoError(t, err)
	embeddedYamlPath := filepath.Join(sourcePath, sdBuildPath, "services.yaml")
	err = os.WriteFile(embeddedYamlPath, yamlBytes, 0644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(embeddedYamlPath) })

	// 2. Pre-create test resources.
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

	// --- Act & Assert ---
	t.Log("Creating Conductor...")
	opts := orchestration.ConductorOptions{
		CheckPrerequisites:     true,
		SetupIAM:               true,
		BuildAndDeployServices: true,
		TriggerRemoteSetup:     true,
		VerifyDataflowIAM:      true,
		VerificationTimeout:    5 * time.Minute,
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

	t.Log("Waiting for verification messages from deployed services...")
	select {
	case err := <-validationChan:
		require.NoError(t, err, "Verification listener received an error")
		logger.Info().Msg("✅ Verification complete. All messages received.")
	case <-ctx.Done():
		t.Fatal("Test context timed out before receiving all verification messages.")
	}
}

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

// createVerificationResources is a test helper that requires a hydrated topic name.
func createVerificationResources(t *testing.T, ctx context.Context, client *pubsub.Client, hydratedTopicName string) (*pubsub.Topic, *pubsub.Subscription) {
	t.Helper()
	verifyTopic := client.Topic(hydratedTopicName)
	exists, err := verifyTopic.Exists(ctx)
	require.NoError(t, err)
	if !exists {
		// This should not happen if the test pre-creates it.
		t.Fatalf("Verification topic %s was expected to exist but was not found.", hydratedTopicName)
	}

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
