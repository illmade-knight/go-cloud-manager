//go:build integration

package iam_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/run/v2"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/pubsub"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestIAMManager_FullFlow(t *testing.T) {
	// --- Arrange ---
	projectID := CheckGCPAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	logger := zerolog.New(os.Stderr).With().Timestamp().Str("test", "TestIAMManager_FullFlow").Logger()

	// 1. Define a sample architecture for the test.
	runID := time.Now().UnixNano()
	testTopicName := fmt.Sprintf("test-topic-for-iam-%d", runID)
	testSecretName := fmt.Sprintf("test-secret-for-iam-%d", runID)
	// This is now a logical name, not a specific SA to be created.
	testSaName := "pooled-iam-test-sa"
	// This prefix must match the one used to create the TestIAMClient.
	testSaPrefix := "it-iam"

	targetServiceName := "target-service"

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-dataflow": {
				Services: map[string]servicemanager.ServiceSpec{
					"test-service": {
						Name:           "test-service",
						ServiceAccount: testSaName,
						Dependencies:   []string{targetServiceName},
						Deployment: &servicemanager.DeploymentSpec{
							SecretEnvironmentVars: []servicemanager.SecretEnvVar{
								{Name: "MY_SECRET", ValueFrom: testSecretName},
							},
						},
					},
					targetServiceName: {
						Name:           targetServiceName,
						ServiceAccount: "pooled-iam-target-sa",
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath:          ".", // Will be overridden by temp dir
							BuildableModulePath: ".",
							ImageRepo:           "gcm-images",
						},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource: servicemanager.CloudResource{
								Name: testTopicName,
								IAMPolicy: []servicemanager.IAM{
									{Name: "test-service", Role: "roles/pubsub.publisher"},
								},
							},
						},
					},
				},
			},
		},
	}

	// 2. CORRECTED: Create the TestIAMClient for pooling instead of the GoogleIAMClient.
	iamClient, err := iam.NewTestIAMClient(ctx, projectID, logger, testSaPrefix)
	require.NoError(t, err)
	// ADDED: Ensure the test client is closed to clean up roles on all SAs in the pool.
	t.Cleanup(func() { iamClient.Close() })

	iamManager := iam.NewIAMManager(iamClient, logger)
	rolePlanner := iam.NewRolePlanner(logger)

	// 3. Ensure prerequisite resources exist (no changes here).
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer psClient.Close()
	topic, err := psClient.CreateTopic(ctx, testTopicName)
	require.NoError(t, err)
	t.Cleanup(func() { topic.Delete(context.Background()) })

	secretClient, err := secretmanager.NewClient(ctx)
	require.NoError(t, err)
	defer secretClient.Close()
	secret, err := servicemanager.EnsureSecretExistsWithValue(ctx, secretClient, projectID, testSecretName, "test-data")
	require.NoError(t, err, "Failed to set up test secret")
	t.Cleanup(func() {
		secretPath := fmt.Sprintf("projects/%s/secrets/%s", projectID, testSecretName)
		err = secretClient.DeleteSecret(context.Background(), &secretmanagerpb.DeleteSecretRequest{Name: secretPath})
		if err != nil {
			logger.Warn().Msg("failed to delete secret")
		}
	})

	// --- Act ---
	t.Log("Planning IAM roles for application services...")
	plan, err := rolePlanner.PlanRolesForApplicationServices(ctx, arch)
	require.NoError(t, err)
	require.Contains(t, plan, testSaName, "Plan should include the test service account")

	// ADDED: Verify the plan includes the Cloud Run Invoker role.
	t.Log("Verifying the role plan content...")
	bindings := plan[testSaName]
	require.Len(t, bindings, 3, "The plan should contain exactly three bindings for the service")

	var foundInvoker bool
	for _, binding := range bindings {
		if binding.ResourceType == "cloudrun_service" && binding.ResourceID == targetServiceName && binding.Role == "roles/run.invoker" {
			foundInvoker = true
		}
	}
	require.True(t, foundInvoker, "Plan should include the run.invoker role on the target service")

	t.Log("Applying IAM policies for the dataflow...")
	err = iamManager.ApplyIAMForService(ctx, arch.Dataflows["test-dataflow"], "test-service")
	require.NoError(t, err)

	// --- Assert ---
	t.Log("Verifying the IAM policies were set correctly...")

	require.Eventually(t, func() bool {
		// Verify Pub/Sub policy
		topicPolicy, err := topic.IAM().Policy(ctx)
		if err != nil {
			return false // Retry if we can't get the policy
		}
		foundPubSubBinding := false
		for _, member := range topicPolicy.Members("roles/pubsub.publisher") {
			if strings.HasPrefix(member, "serviceAccount:"+testSaPrefix) {
				foundPubSubBinding = true
				break
			}
		}

		// Verify Secret Manager policy
		secretName := strings.Split(secret.Name, "/versions/")[0]

		secretPolicy, err := secretClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: secretName})
		if err != nil {
			return false // Retry if we can't get the policy
		}
		foundSecretBinding := false
		for _, binding := range secretPolicy.Bindings {
			if binding.Role == "roles/secretmanager.secretAccessor" {
				for _, member := range binding.Members {
					if strings.HasPrefix(member, "serviceAccount:"+testSaPrefix) {
						foundSecretBinding = true
						break
					}
				}
			}
		}

		runService, err := run.NewService(ctx) // This client is needed for verification
		if err != nil {
			return false
		}
		fullSvcName := fmt.Sprintf("projects/%s/locations/%s/services/%s", projectID, arch.Region, targetServiceName)
		runPolicy, err := runService.Projects.Locations.Services.GetIamPolicy(fullSvcName).Do()
		if err != nil {
			return false
		}

		foundRunBinding := false
		for _, binding := range runPolicy.Bindings {
			if binding.Role == "roles/run.invoker" {
				for _, member := range binding.Members {
					if strings.HasPrefix(member, "serviceAccount:"+testSaPrefix) {
						foundRunBinding = true
						break
					}
				}
			}
		}

		_ = foundRunBinding

		// The function returns true only when ALL bindings are found.
		return foundPubSubBinding && foundSecretBinding
	}, 30*time.Second, 3*time.Second, "IAM policies did not propagate in time")

	t.Log("✅ Verification successful.")

	// --- Cleanup ---
	// CORRECTED: Explicit cleanup of the SA and its roles is no longer needed.
	// The `iamClient.Close()` call registered with `t.Cleanup` handles returning the
	// SA to the pool and wiping its IAM roles automatically.
	t.Log("Cleanup will be handled automatically by TestIAMClient.Close()")
}
