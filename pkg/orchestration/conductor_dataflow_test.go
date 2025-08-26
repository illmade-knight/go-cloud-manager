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

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

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
	pubName, pubServiceAccount := "tracer-publisher", "pub-sa"
	subName, subServiceAccount := "tracer-subscriber", "sub-sa"
	verifyTopicName, tracerTopicName, tracerSubName := "verify-topic", "tracer-topic", "tracer-sub"

	// 1. Define Architecture with logical names.
	arch = &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{Name: "dataflow-test", ProjectID: projectID, Region: "us-central1"},
		ServiceManagerSpec: servicemanager.ServiceManagerSpec{
			ServiceSpec: servicemanager.ServiceSpec{
				Name:           sdName,
				ServiceAccount: sdServiceAccount,
				Deployment: &servicemanager.DeploymentSpec{
					SourcePath:          sourcePath,
					BuildableModulePath: sdBuildPath,
				},
			},
			CommandTopic: servicemanager.ServiceMapping{
				Name: "command-topic",
				Lookup: servicemanager.Lookup{
					Key:    "command-topic-id",
					Method: servicemanager.LookupYAML,
				},
			},
			CompletionTopic: servicemanager.ServiceMapping{
				Name: "completion-topic",
				Lookup: servicemanager.Lookup{
					Key:    "completion-topic-id",
					Method: servicemanager.LookupYAML,
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
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
									Key:    "tracer-topic-id",
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
									Key:    "verify-topic-id",
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
								Key:    "tracer-subscription-id",
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

	// 4. Pre-create test resources.
	t.Log("Pre-creating test resources...")
	hydratedVerifyTopicName := nameMap[verifyTopicName]
	hydratedTracerTopicName := nameMap[tracerTopicName]

	// google is having a laugh with this nonsense isn't it???
	qualifiedVerifyTopicName := fmt.Sprintf("projects/%s/topics/%s", projectID, hydratedVerifyTopicName)
	qualifiedTracerTopicName := fmt.Sprintf("projects/%s/topics/%s", projectID, hydratedTracerTopicName)

	verifyTopic, err := psClient.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{
		Name: qualifiedVerifyTopicName,
	})
	require.NoError(t, err)
	t.Cleanup(
		func() {
			_ = psClient.TopicAdminClient.DeleteTopic(context.Background(), &pubsubpb.DeleteTopicRequest{Topic: verifyTopic.Name})
		})

	tracerTopic, err := psClient.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{
		Name: qualifiedTracerTopicName,
	})
	require.NoError(t, err)
	t.Cleanup(
		func() {
			_ = psClient.TopicAdminClient.DeleteTopic(context.Background(), &pubsubpb.DeleteTopicRequest{Topic: tracerTopic.Name})
		})
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

	// 3. NEW: Run the local preflight check before any cloud deployment.
	//preflightServiceConfigs(t, arch)

	verifySubName := fmt.Sprintf("verify-sub-%s", uuid.New().String()[:8])
	qualifiedVerifySubName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, verifySubName)
	verifySub := createVerificationResources(t, ctx, psClient, qualifiedVerifySubName, verifyTopic.Name)
	subscriber := psClient.Subscriber(verifySub.Name)
	validationChan := startVerificationListener(t, ctx, subscriber, 2)

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
func startVerificationListener(t *testing.T, ctx context.Context, sub *pubsub.Subscriber, expectedCount int) <-chan error {
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
func createVerificationResources(t *testing.T, ctx context.Context, client *pubsub.Client, subName, hydratedTopicName string) *pubsubpb.Subscription {
	t.Helper()

	verifySub, err := client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               subName,
		Topic:              hydratedTopicName,
		AckDeadlineSeconds: 10,
		MessageRetentionDuration: &durationpb.Duration{
			Seconds: 10 * 60,
			Nanos:   0,
		},
	})

	require.NoError(t, err)

	return verifySub
}
