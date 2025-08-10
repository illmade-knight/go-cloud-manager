//go:build integration

package iam_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	gcpiam "cloud.google.com/go/iam"
	"cloud.google.com/go/pubsub"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-test/auth"
	"github.com/stretchr/testify/require"
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
		defer c.Close()
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

// TestApplyIAMPolicy_AtomicUpdate verifies that the ApplyIAMPolicy method
// correctly overwrites the entire policy on a resource.
func TestApplyIAMPolicy_AtomicUpdate(t *testing.T) {
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
