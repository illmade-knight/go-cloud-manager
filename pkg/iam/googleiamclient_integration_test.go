//go:build integration

package iam_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	gcpiam "cloud.google.com/go/iam"
	"cloud.google.com/go/pubsub"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isRetriableError centralizes the logic for identifying transient gRPC errors.
func isRetriableError(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unavailable, codes.ResourceExhausted, codes.Unauthenticated:
		return true
	default:
		return false
	}
}

// executeWithRetry wraps any function in a retry loop for test setup stability.
func executeWithRetry(t *testing.T, operationName string, operation func() error) {
	t.Helper()
	const maxRetries = 5
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		err := operation()
		if err == nil {
			return // Success
		}
		lastErr = err
		if isRetriableError(err) {
			t.Logf("Operation '%s' failed with transient error, retrying...", operationName)
			time.Sleep(time.Duration(i*100+50) * time.Millisecond)
			continue
		}
		require.NoError(t, err, "Operation '%s' failed with a non-retriable error", operationName)
		return
	}
	require.NoError(t, lastErr, "Operation '%s' failed after all retries", operationName)
}

// getProjectNumber retrieves the project number for the current project.
func getProjectNumber(t *testing.T, ctx context.Context, projectID string) string {
	t.Helper()
	var projectNumber string
	operation := func() error {
		c, err := resourcemanager.NewProjectsClient(ctx)
		if err != nil {
			return err
		}
		defer func() {
			_ = c.Close()
		}()
		req := &resourcemanagerpb.GetProjectRequest{Name: fmt.Sprintf("projects/%s", projectID)}
		project, err := c.GetProject(ctx, req)
		if err != nil {
			return err
		}
		projectNumber = strings.TrimPrefix(project.Name, "projects/")
		return nil
	}
	executeWithRetry(t, "getProjectNumber", operation)
	return projectNumber
}

// TestApplyIAMPolicy_CloudRun_AdditiveUpdate verifies that ApplyIAMPolicy is additive for Cloud Run services.
func TestApplyIAMPolicy_CloudRun_AdditiveUpdate(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	region := "us-central1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// REFACTOR: Use the TestIAMClient to lease from a pool of service accounts.
	// This avoids creating new SAs on every run and respects GCP quotas.
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	iamClient, err := iam.NewTestIAMClient(ctx, projectID, logger, "it-") // "it-" for "integration-test"
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	runService, err := run.NewService(ctx)
	require.NoError(t, err)

	// 1. Lease a temporary Service Account from the pool to be granted the invoker role.
	// REFACTOR: The name is now a short, valid-length logical name. The TestIAMClient
	// handles leasing or creating an actual SA with a valid name.
	logicalInvokerSaName := fmt.Sprintf("invoker-%d", time.Now().Unix())
	invokerSaEmail, err := iamClient.EnsureServiceAccountExists(ctx, logicalInvokerSaName)
	require.NoError(t, err)

	// The TestIAMClient's overridden DeleteServiceAccount will return the SA to the pool instead of deleting it.
	t.Cleanup(func() {
		err := iamClient.DeleteServiceAccount(context.Background(), invokerSaEmail)
		if err != nil {
			t.Logf("Failed to return service account %s to pool: %v", invokerSaEmail, err)
		}
	})
	t.Logf("Leased temporary invoker SA: %s", invokerSaEmail)

	// 2. Create a dummy Cloud Run service to apply the policy to.
	serviceName := fmt.Sprintf("test-iam-target-svc-%d", time.Now().Unix())
	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", projectID, region, serviceName)
	parent := fmt.Sprintf("projects/%s/locations/%s", projectID, region)

	createOp, err := runService.Projects.Locations.Services.Create(parent, &run.GoogleCloudRunV2Service{
		Template: &run.GoogleCloudRunV2RevisionTemplate{
			Containers: []*run.GoogleCloudRunV2Container{
				{Image: "us-docker.pkg.dev/cloudrun/container/hello"},
			},
		},
	}).ServiceId(serviceName).Do()
	require.NoError(t, err)

	t.Cleanup(func() {
		// Use a background context for cleanup in case the test context is cancelled.
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer bgCancel()
		deleteOp, err := runService.Projects.Locations.Services.Delete(fullServiceName).Context(bgCtx).Do()
		if err != nil {
			if status.Code(err) != codes.NotFound {
				t.Logf("Failed to start cleanup for Cloud Run service %s: %v", serviceName, err)
			}
			return
		}
		// Wait for deletion to complete.
		for !deleteOp.Done {
			select {
			case <-bgCtx.Done():
				t.Logf("Cleanup timed out for Cloud Run service %s", serviceName)
				return
			case <-time.After(2 * time.Second):
				deleteOp, _ = runService.Projects.Locations.Operations.Get(deleteOp.Name).Context(bgCtx).Do()
			}
		}
		t.Logf("Cleaned up Cloud Run service: %s", serviceName)
	})

	// Wait for the service creation operation to complete.
	t.Logf("Waiting for Cloud Run service '%s' to be created...", serviceName)
	for !createOp.Done {
		select {
		case <-ctx.Done():
			t.Fatal("Test timed out waiting for Cloud Run service creation")
		case <-time.After(2 * time.Second):
			createOp, err = runService.Projects.Locations.Operations.Get(createOp.Name).Do()
			require.NoError(t, err)
		}
	}
	t.Logf("✅ Cloud Run service '%s' created.", serviceName)

	// 3. Define the desired IAM policy binding.
	member := "serviceAccount:" + invokerSaEmail
	role := "roles/run.invoker"
	desiredPolicy := iam.PolicyBinding{
		ResourceType:     "cloudrun_service",
		ResourceID:       serviceName,
		ResourceLocation: region,
		MemberRoles: map[string][]string{
			role: {member},
		},
	}

	// --- Act ---
	t.Logf("Applying additive policy to Cloud Run service with role '%s' for member '%s'...", role, member)
	err = iamClient.ApplyIAMPolicy(ctx, desiredPolicy)
	require.NoError(t, err)

	// --- Assert ---
	t.Log("Verifying IAM binding existence (polling)...")
	checkBinding := iam.IAMBinding{ResourceType: "cloudrun_service", ResourceID: serviceName, Role: role, ResourceLocation: region}
	require.Eventually(t, func() bool {
		found, err := iamClient.CheckResourceIAMBinding(ctx, checkBinding, member)
		if err != nil && !isRetriableError(err) {
			t.Logf("Non-retriable error during check: %v", err)
			return false
		}
		return found
	}, 2*time.Minute, 5*time.Second, "IAM binding for Cloud Run invoker did not propagate in time")

	t.Log("✅ Cloud Run invoker role verified.")

	// --- Act & Assert 2: Removal ---
	t.Logf("Removing role '%s' from member '%s'...", role, member)
	err = iamClient.RemoveResourceIAMBinding(ctx, checkBinding, member)
	require.NoError(t, err)

	t.Log("Verifying binding removal (polling)...")
	require.Eventually(t, func() bool {
		found, err := iamClient.CheckResourceIAMBinding(ctx, checkBinding, member)
		if err != nil && !isRetriableError(err) {
			t.Logf("Non-retriable error during check: %v", err)
			return false
		}
		return !found
	}, 2*time.Minute, 5*time.Second, "IAM binding for Cloud Run invoker was not removed in time")

	t.Log("✅ Cloud Run invoker role removal verified.")
}

// TestApplyIAMPolicy_AtomicUpdate verifies that the ApplyIAMPolicy method
// correctly overwrites the entire policy on a resource.
func TestApplyIAMPolicy_AtomicUpdate(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Use the real client for this test to ensure atomic overwrite behavior is tested directly.
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	topicName := fmt.Sprintf("test-atomic-iam-topic-%d", time.Now().UnixNano())
	var topic *pubsub.Topic
	executeWithRetry(t, "CreatePubSubTopic", func() error {
		var createErr error
		topic, createErr = psClient.CreateTopic(ctx, topicName)
		return createErr
	})
	require.NotNil(t, topic)
	t.Cleanup(func() { _ = topic.Delete(context.Background()) })

	member := fmt.Sprintf("serviceAccount:%s-compute@developer.gserviceaccount.com", projectNumber)
	rogueRole := "roles/pubsub.editor"
	desiredRole := "roles/pubsub.viewer"

	// 1. Pre-condition: Add a rogue role that should be removed by the atomic update.
	t.Logf("Applying rogue role '%s' to be overwritten...", rogueRole)
	err = iamClient.AddResourceIAMBinding(ctx, iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: topicName, Role: rogueRole}, member)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		found, _ := iamClient.CheckResourceIAMBinding(ctx, iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: topicName, Role: rogueRole}, member)
		return found
	}, 1*time.Minute, 2*time.Second, "Rogue role failed to apply")
	t.Log("Rogue role applied.")

	// 2. Define the desired final state of the policy.
	desiredPolicy := iam.PolicyBinding{
		ResourceType: "pubsub_topic",
		ResourceID:   topicName,
		MemberRoles: map[string][]string{
			desiredRole: {member},
		},
	}

	// --- Act ---
	t.Log("Applying the desired policy atomically with ApplyIAMPolicy...")
	err = iamClient.ApplyIAMPolicy(ctx, desiredPolicy)
	require.NoError(t, err)

	// --- Assert ---
	t.Log("Verifying final policy state...")
	require.Eventually(t, func() bool {
		policy, err := topic.IAM().Policy(ctx)
		if err != nil {
			return false
		}
		hasDesiredRole := policy.HasRole(member, gcpiam.RoleName(desiredRole))
		hasRogueRole := policy.HasRole(member, gcpiam.RoleName(rogueRole))
		return hasDesiredRole && !hasRogueRole
	}, 2*time.Minute, 5*time.Second, "The final policy state was not what was expected.")

	t.Log("✅ Atomic update verified. Policy is in the correct final state.")
}

// TestCheckResourceIAMBinding_PubSub verifies the add, check, and remove lifecycle for Pub/Sub IAM bindings.
func TestCheckResourceIAMBinding_PubSub(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	topicName := fmt.Sprintf("test-iam-check-topic-%d", time.Now().UnixNano())
	var topic *pubsub.Topic
	executeWithRetry(t, "CreatePubSubTopic", func() error {
		var createErr error
		topic, createErr = psClient.CreateTopic(ctx, topicName)
		return createErr
	})
	require.NotNil(t, topic)
	t.Cleanup(func() { _ = topic.Delete(context.Background()) })

	subName := fmt.Sprintf("test-iam-check-sub-%d", time.Now().UnixNano())
	var sub *pubsub.Subscription
	executeWithRetry(t, "CreatePubSubSubscription", func() error {
		var createErr error
		sub, createErr = psClient.CreateSubscription(ctx, subName, pubsub.SubscriptionConfig{Topic: topic})
		return createErr
	})
	require.NotNil(t, sub)
	t.Cleanup(func() { _ = sub.Delete(context.Background()) })

	member := fmt.Sprintf("serviceAccount:%s-compute@developer.gserviceaccount.com", projectNumber)
	role := "roles/pubsub.viewer"
	topicBinding := iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: topicName, Role: role}
	subBinding := iam.IAMBinding{ResourceType: "pubsub_subscription", ResourceID: subName, Role: role}

	// --- Act & Assert ---
	t.Log("Verifying initial state: binding should not exist...")
	found, err := iamClient.CheckResourceIAMBinding(ctx, topicBinding, member)
	require.NoError(t, err)
	require.False(t, found, "Binding should not exist initially for topic")

	found, err = iamClient.CheckResourceIAMBinding(ctx, subBinding, member)
	require.NoError(t, err)
	require.False(t, found, "Binding should not exist initially for subscription")
	t.Log("✅ Initial state verified.")

	t.Logf("Adding role '%s' to member '%s'...", role, member)
	err = iamClient.AddResourceIAMBinding(ctx, topicBinding, member)
	require.NoError(t, err)
	err = iamClient.AddResourceIAMBinding(ctx, subBinding, member)
	require.NoError(t, err)

	t.Log("Verifying binding existence (polling)...")
	require.Eventually(t, func() bool {
		found, err = iamClient.CheckResourceIAMBinding(ctx, topicBinding, member)
		return err == nil && found
	}, 2*time.Minute, 5*time.Second, "Binding for topic did not propagate in time")

	require.Eventually(t, func() bool {
		found, err = iamClient.CheckResourceIAMBinding(ctx, subBinding, member)
		return err == nil && found
	}, 2*time.Minute, 5*time.Second, "Binding for subscription did not propagate in time")
	t.Log("✅ Binding existence verified.")

	t.Logf("Removing role '%s' from member '%s'...", role, member)
	err = iamClient.RemoveResourceIAMBinding(ctx, topicBinding, member)
	require.NoError(t, err)
	err = iamClient.RemoveResourceIAMBinding(ctx, subBinding, member)
	require.NoError(t, err)

	t.Log("Verifying binding removal (polling)...")
	require.Eventually(t, func() bool {
		found, err = iamClient.CheckResourceIAMBinding(ctx, topicBinding, member)
		return err == nil && !found
	}, 2*time.Minute, 5*time.Second, "Binding for topic was not removed in time")

	require.Eventually(t, func() bool {
		found, err = iamClient.CheckResourceIAMBinding(ctx, subBinding, member)
		return err == nil && !found
	}, 2*time.Minute, 5*time.Second, "Binding for subscription was not removed in time")
	t.Log("✅ Binding removal verified.")
}
