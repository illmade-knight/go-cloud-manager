//go:build cloud_integration

package deployment_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/iam/admin/apiv1"
	"cloud.google.com/go/iam/admin/apiv1/adminpb"
	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// checkGCPAuth is a helper that fails fast if the test is not configured to run.
func checkGCPAuth(t *testing.T) string {
	t.Helper()
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		t.Skip("Skipping real integration test: GCP_PROJECT_ID environment variable is not set")
	}
	// A simple adminClient creation is enough to check basic auth config
	// without performing a full API call like listing resources.
	_, err := pubsub.NewClient(context.Background(), projectID)
	if err != nil {
		t.Fatalf(`
		---------------------------------------------------------------------
		GCP AUTHENTICATION FAILED!
		---------------------------------------------------------------------
		Could not create a Google Cloud adminClient. This is likely due to
		expired or missing Application Default Credentials (ADC).

		To fix this, please run 'gcloud auth application-default login'.

		Original Error: %v
		---------------------------------------------------------------------
		`, err)
	}
	return projectID
}

func TestGoogleIAMClient_RealIntegration_FullLifecycle(t *testing.T) {
	projectID := checkGCPAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// --- ARRANGE ---
	// 1. Create the real GoogleIAMClient instance we are testing.
	iamClient, err := deployment.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)

	// 2. Create other necessary real clients for resource creation and cleanup.
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer psClient.Close()

	adminClient, err := admin.NewIamClient(ctx)
	require.NoError(t, err)
	defer adminClient.Close()

	// 3. Define unique names for our temporary test resources.
	runID := uuid.New().String()[:8]
	saName := fmt.Sprintf("it-iam-sa-%s", runID)
	topicName := fmt.Sprintf("it-iam-topic-%s", runID)
	saResourcePath := fmt.Sprintf("projects/%s/serviceAccounts/%s@%s.iam.gserviceaccount.com", projectID, saName, projectID)

	// 4. CRITICAL: Defer the cleanup of all resources created during the test.
	t.Cleanup(func() {
		t.Logf("Cleaning up test resources for run %s...", runID)
		// Delete the Pub/Sub topic
		if topic := psClient.Topic(topicName); topic != nil {
			if err := topic.Delete(context.Background()); err == nil {
				t.Logf("Deleted topic: %s", topicName)
			}
		}
		// Delete the Service Account
		err := adminClient.DeleteServiceAccount(context.Background(), &adminpb.DeleteServiceAccountRequest{Name: saResourcePath})
		if err == nil {
			t.Logf("Deleted service account: %s", saName)
		} else if status.Code(err) != codes.NotFound {
			// Log error only if it's not a "not found" error
			t.Logf("Could not clean up service account %s: %v", saName, err)
		}
	})

	// --- ACT ---
	// 1. Ensure the service account exists. This should create it.
	saEmail, err := iamClient.EnsureServiceAccountExists(ctx, saName)
	require.NoError(t, err)
	require.NotEmpty(t, saEmail)
	t.Logf("Successfully created or found service account: %s", saEmail)

	// 2. Create the target resource (a Pub/Sub topic) that we'll apply policy to.
	topic, err := psClient.CreateTopic(ctx, topicName)
	require.NoError(t, err)
	t.Logf("Successfully created topic: %s", topicName)

	// 3. Use our adminClient to add the IAM binding. This is the main action we are testing.
	member := "serviceAccount:" + saEmail
	role := "roles/pubsub.publisher"
	err = iamClient.AddResourceIAMBinding(ctx, "pubsub_topic", topicName, role, member)
	require.NoError(t, err, "AddResourceIAMBinding should succeed")
	t.Logf("Applied IAM binding: %s on %s for member %s", role, topicName, member)

	// --- ASSERT ---
	// 4. Verify the permission was applied correctly using the official TestIamPermissions method.
	var permissions []string
	permissions = append(permissions, "pubsub.topics.publish")

	// We need to wait a moment for IAM policies to propagate.
	var allowed []string
	require.Eventually(t, func() bool {
		allowed, err = topic.IAM().TestPermissions(ctx, permissions)
		// Return true if the call succeeded and we got the permission we expected.
		return err == nil && len(allowed) == len(permissions)
	}, 20*time.Second, 2*time.Second, "IAM policy did not propagate in time")

	require.NoError(t, err)
	assert.Len(t, allowed, 1, "Should have one allowed permission")
	assert.Equal(t, "pubsub.topics.publish", allowed[0], "The 'publish' permission should now be granted")
	t.Log("Verification successful: Service account has the correct permissions.")
}
