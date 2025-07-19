//go:build integration

package iam_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAMManager_FullFlow performs a full, real-world IAM setup and verification.
// It uses a mock architecture to plan roles, creates a real service account,
// applies the roles, and then verifies they were set correctly before cleaning up.
func TestIAMManager_FullFlow(t *testing.T) {
	// --- Arrange ---
	projectID := CheckGCPAuth(t) // Uses the helper from your iam package
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	logger := zerolog.New(os.Stderr).With().Timestamp().Str("test", "TestIAMManager_FullFlow").Logger()

	// 1. Define a sample architecture for the test.
	testTopicName := fmt.Sprintf("test-topic-for-iam-%d", time.Now().UnixNano())
	testSaName := "test-iam-sa"

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			ProjectID: projectID,
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-dataflow": {
				Services: map[string]servicemanager.ServiceSpec{
					"test-service": {
						Name:           "test-service",
						ServiceAccount: testSaName,
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

	// 2. Create the IAM clients and planners.
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)

	iamManager := iam.NewIAMManager(iamClient, logger)
	rolePlanner := iam.NewRolePlanner(logger)

	// 3. Ensure the test topic exists so we can set a policy on it.
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer psClient.Close()
	topic, err := psClient.CreateTopic(ctx, testTopicName)
	require.NoError(t, err)

	// --- Act ---
	// 4. Plan the roles required for the application services.
	t.Log("Planning IAM roles for application services...")
	plan, err := rolePlanner.PlanRolesForApplicationServices(ctx, arch)
	require.NoError(t, err)
	require.Contains(t, plan, testSaName, "Plan should include the test service account")

	// 5. Apply the IAM policies using the IAMManager.
	t.Log("Applying IAM policies for the dataflow...")
	err = iamManager.ApplyIAMForService(ctx, arch.Dataflows["test-dataflow"], "test-service")
	require.NoError(t, err)

	// --- Assert ---
	// 6. Directly verify that the role was applied correctly in GCP.
	t.Log("Verifying the IAM policy was set correctly...")
	saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", testSaName, projectID)
	member := "serviceAccount:" + saEmail

	policy, err := topic.IAM().Policy(ctx)
	require.NoError(t, err)
	assert.Contains(t, policy.Members("roles/pubsub.publisher"), member, "The service account should be a publisher on the topic")
	t.Log("✅ Verification successful.")

	// --- Cleanup ---
	t.Log("Cleaning up test resources...")
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		// CORRECTED: Use the "get, modify, set" pattern to remove the IAM binding.
		policy, err := topic.IAM().Policy(cleanupCtx)
		if err == nil {
			policy.Remove(member, "roles/pubsub.publisher")
			topic.IAM().SetPolicy(cleanupCtx, policy)
		}

		// Clean up the topic.
		topic.Delete(cleanupCtx)

		// Clean up the service account.
		// Note: The EnsureServiceAccountExists creates the SA, so we must also clean it up.
		err = iamClient.DeleteServiceAccount(cleanupCtx, testSaName)
		if err != nil {
			t.Logf("Cleanup warning: failed to delete service account %s: %v", testSaName, err)
		}
	})
}
