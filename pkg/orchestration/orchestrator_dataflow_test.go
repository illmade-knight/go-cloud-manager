//go:build integration

package orchestration_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// Add this alongside the existing 'usePool' flag
var expectedMessages = flag.Int("expected-messages", 1, "Number of messages the verifier should expect to receive.")

// Define a flag to enable the service account pool for testing.
var usePool = flag.Bool("use-pool", false, "Use a pool of service accounts for testing to avoid quota issues.")

func TestOrchestrator_DataflowE2E(t *testing.T) {
	projectID := CheckGCPAuth(t)
	require.NotEmpty(t, projectID, "GCP_PROJECT_ID environment variable must be set")

	region := "us-central1"
	imageRepo := "test-images"
	logger := log.With().Str("test", "TestOrchestrator_DataflowE2E").Logger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if *usePool {
		logger.Info().Msg("✅ Test running in POOLED mode.")
		t.Setenv("TEST_SA_POOL_MODE", "true")
		t.Setenv("TEST_SA_POOL_PREFIX", "it-")
		t.Setenv("TEST_SA_POOL_NO_CREATE", "true")
	} else {
		logger.Info().Msg("Running in STANDARD mode (creating new service accounts).")
	}

	// --- 1. Arrange ---
	runID := uuid.New().String()[:8]
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
					"PROJECT_ID":              projectID,
					"SD_COMMAND_TOPIC":        commandTopicID,
					"SD_COMMAND_SUBSCRIPTION": commandSubID,
					"SD_COMPLETION_TOPIC":     completionTopicID,
					"TRACER_TOPIC_ID":         tracerTopicName,
					"TRACER_SUB_ID":           tracerSubName,
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
						{
							CloudResource: servicemanager.CloudResource{Name: verificationTopicName},
						},
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
	require.NoError(t, servicemanager.HydrateArchitecture(arch, imageRepo, ""))

	// --- 2. Setup Verification and Orchestrators ---
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer psClient.Close()

	validationChan, verifySub := setupVerificationListener(t, ctx, psClient, verificationTopicName, *expectedMessages)

	t.Cleanup(func() { _ = verifySub.Delete(context.Background()) }) // Ensure subscription is deleted

	iamOrch, err := orchestration.NewIAMOrchestrator(ctx, arch, logger)
	require.NoError(t, err)
	orch, err := orchestration.NewOrchestrator(ctx, arch, logger)
	require.NoError(t, err)

	t.Cleanup(func() {
		cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cCancel()
		logger.Info().Msg("--- Starting Full Teardown ---")

		// Tear down all services with the new, clear functions.
		if err := orch.TeardownDataflowServices(cCtx, "tracer-flow"); err != nil {
			logger.Error().Err(err).Msg("Dataflow services teardown failed")
		}
		if err := orch.TeardownCloudRunService(cCtx, sdName); err != nil {
			logger.Error().Err(err).Msg("Service director teardown failed")
		}

		// Teardown orchestrator and IAM resources
		if err := orch.Teardown(cCtx); err != nil {
			logger.Error().Err(err).Msg("Orchestrator teardown failed")
		}
		if err := iamOrch.Teardown(cCtx); err != nil {
			logger.Error().Err(err).Msg("IAM teardown failed")
		}
		logger.Info().Msg("--- Teardown Complete ---")
	})

	// --- 3. Execute Deployment Workflow ---
	sdSaEmails, err := iamOrch.SetupServiceDirectorIAM(ctx)
	require.NoError(t, err)
	directorURL, err := orch.DeployServiceDirector(ctx, sdSaEmails)
	require.NoError(t, err)
	err = orch.AwaitServiceReady(ctx, sdName)
	require.NoError(t, err, "Did not receive 'service_ready' event in time")
	err = orch.TriggerDataflowSetup(ctx, "tracer-flow")
	require.NoError(t, err)
	err = orch.AwaitDataflowReady(ctx, "tracer-flow")
	require.NoError(t, err)
	dfSaEmails, err := iamOrch.SetupDataflowIAM(ctx)
	require.NoError(t, err)

	allSaEmails := make(map[string]string)
	for k, v := range sdSaEmails {
		allSaEmails[k] = v
	}
	for k, v := range dfSaEmails {
		allSaEmails[k] = v
	}

	err = orch.DeployDataflowServices(ctx, "tracer-flow", allSaEmails, directorURL)
	require.NoError(t, err)
	t.Log("Application services deployed.")

	// --- 4. Verify the Live Dataflow ---
	//traceID := uuid.New().String()
	//publisherSvcSpec := arch.Dataflows["tracer-flow"].Services[pubName]
	//require.NoError(t, orch.AwaitRevisionReady(ctx, publisherSvcSpec), "Tracer Publisher never became ready")

	// NOT USED CURRENTLY
	//authClient, err := idtoken.NewClient(ctx, publisherSvcSpec.Deployment.ServiceURL)
	//require.NoError(t, err, "Failed to create authenticated client")
	//
	//resp, err := authClient.Post(fmt.Sprintf("%s?trace_id=%s", publisherSvcSpec.Deployment.ServiceURL, traceID), "application/json", nil)
	//require.NoError(t, err)
	//require.Equal(t, http.StatusOK, resp.StatusCode)
	//resp.Body.Close()
	//t.Logf("Successfully sent tracer message with ID: %s", traceID)

	// --- 4. Verify the Live Dataflow ---
	// The old verification logic is replaced with this new block.
	t.Logf("Waiting to receive %d verification message(s)...", *expectedMessages)
	select {
	case err := <-validationChan:
		require.NoError(t, err, "Verification failed while receiving messages")
		t.Logf("✅ Verification successful! Received %d message(s).", *expectedMessages)
	case <-time.After(3 * time.Minute): // An overall safety timeout for the test.
		t.Fatalf("Timeout: Did not complete verification for %d messages in time.", *expectedMessages)
	}
}

// CheckGCPAuth is a helper that fails fast if the test is not configured to run
// with valid Application Default Credentials (ADC) that can invoke Cloud Run.
func CheckGCPAuth(t *testing.T) string {
	t.Helper()
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		t.Skip("Skipping real integration test: GCP_PROJECT_ID environment variable is not set")
	}
	ctx := context.Background()

	// 1. Check basic connectivity and authentication for resource management.
	_, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		t.Fatalf(`
		---------------------------------------------------------------------
		GCP AUTHENTICATION FAILED!
		---------------------------------------------------------------------
		Could not create a Google Cloud client. This is likely due to
		expired or missing Application Default Credentials (ADC).

		To fix this, please run 'gcloud auth application-default login'.

		Original Error: %v
		---------------------------------------------------------------------
		`, err)
	}

	//// Log the principal associated with the Application Default Credentials.
	//creds, err := google.FindDefaultCredentials(ctx)
	//if err == nil {
	//	// Attempt to unmarshal the JSON to find the principal.
	//	var credsMap map[string]interface{}
	//	if json.Unmarshal(creds.JSON, &credsMap) == nil {
	//		if clientEmail, ok := credsMap["client_email"]; ok {
	//			t.Logf("--- Using GCP Service Account: %s", clientEmail)
	//		} else {
	//			// For user credentials, the file path is the most reliable identifier.
	//			t.Logf("--- Using GCP User Credentials from file: %s", os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	//		}
	//	}
	//}
	//
	// TODO introduce this check if we setup a dedicated test runner service account
	//// 2. Check for the specific ability to create ID tokens, which is needed to invoke Cloud Run.
	//// This check validates the credential type without making a network call.
	//_, err = idtoken.NewTokenSource(ctx, "https://example.com")
	//if err != nil && strings.Contains(err.Error(), "unsupported credentials type") {
	//	// This is the specific error the user is seeing. Provide a detailed, actionable fix.
	//	t.Fatalf(`
	//	---------------------------------------------------------------------
	//	GCP INVOCATION AUTHENTICATION FAILED!
	//	---------------------------------------------------------------------
	//	The test failed because your Application Default Credentials (ADC)
	//	are user credentials, which cannot be used by this client library to
	//	invoke secure Cloud Run services directly.
	//
	//	To fix this, the user running the test needs the permission to invoke
	//	Cloud Run services.
	//
	//	SOLUTION: Grant your user the 'Cloud Run Invoker' role on the project.
	//	   1. Find your user email by running:
	//	      gcloud auth list --filter=status:ACTIVE --format="value(account)"
	//	   2. Grant the role by running (replace [YOUR_EMAIL] and [YOUR_PROJECT]):
	//	      gcloud projects add-iam-policy-binding %s --member="user:[YOUR_EMAIL]" --role="roles/run.invoker"
	//
	//	After granting the permission, you may need to refresh your credentials:
	//	gcloud auth application-default login
	//
	//	Original Error: %v
	//	---------------------------------------------------------------------
	//	`, projectID, err)
	//} else if err != nil {
	//	// A different, unexpected token-related error occurred.
	//	t.Fatalf("Failed to create an ID token source, please check your GCP auth: %v", err)
	//}
	//
	return projectID
}

// in orchestrator_dataflow_test.go

// setupVerificationListener creates a subscription and returns a channel that will receive
// a single error. A nil error indicates the expected number of valid messages were received.
// in orchestrator_dataflow_test.go

// setupVerificationListener creates a subscription and returns a channel that will receive
// a single error. A nil error indicates the expected number of valid messages were received.
func setupVerificationListener(t *testing.T, ctx context.Context, client *pubsub.Client, topicName string, expectedCount int) (<-chan error, *pubsub.Subscription) {
	t.Helper()
	// ... (topic and subscription creation remains the same) ...
	verifyTopic, err := client.CreateTopic(ctx, topicName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = verifyTopic.Delete(context.Background()) })

	subID := fmt.Sprintf("verify-sub-%s", uuid.New().String()[:8])
	verifySub, err := client.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic:       verifyTopic,
		AckDeadline: 20 * time.Second,
	})
	require.NoError(t, err)

	t.Logf("TEST CONFIG: Verification Topic is [%s]", verifyTopic.ID())
	t.Logf("TEST CONFIG: Verification Subscription is [%s]", verifySub.ID())

	validationChan := make(chan error, 1)
	var receivedCount atomic.Int32

	go func() {
		// The receiver will run for a maximum of 3 minutes.
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()

		err := verifySub.Receive(cctx, func(ctx context.Context, m *pubsub.Message) {
			log.Info().Str("data", string(m.Data)).Msg("Verifier received a message.")
			m.Ack()

			if expectedCount > 1 && !strings.HasPrefix(string(m.Data), "iot-trace-") {
				// Non-blocking write in case the channel is already full.
				select {
				case validationChan <- fmt.Errorf("received message with invalid format: %s", string(m.Data)):
				default:
				}
				cancel()
				return
			}

			if receivedCount.Add(1) == int32(expectedCount) {
				select {
				case validationChan <- nil: // Signal success
				default:
				}
				cancel()
			}
		})

		// --- FIX: This block correctly reports timeouts and other errors ---
		// This logic runs after Receive() returns.
		if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
			// If the context timed out, we must check if success was already signaled.
			// A non-blocking write prevents a deadlock if the channel is full.
			select {
			case validationChan <- fmt.Errorf("verifier timed out while waiting for %d message(s)", expectedCount):
				// Successfully sent the timeout error.
			default:
				// The channel was already written to (likely with a success message), so do nothing.
			}
		} else if err != nil && !errors.Is(err, context.Canceled) {
			// Handle other, unexpected pubsub errors
			select {
			case validationChan <- fmt.Errorf("pubsub receive failed: %w", err):
			default:
			}
		}
	}()

	return validationChan, verifySub
}
