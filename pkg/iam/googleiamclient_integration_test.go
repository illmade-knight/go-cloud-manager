//go:build integration

package iam_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"

	// REFACTOR: Aliased the official Google Cloud IAM package to avoid naming conflicts
	// with the local iam package ('github.com/illmade-knight/go-cloud-manager/pkg/iam').
	gcpiam "cloud.google.com/go/iam"
	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/pubsub"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// REFACTOR: This helper function centralizes the logic for identifying
// transient gRPC errors that are safe to retry.
func isRetriableError(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		// Not a gRPC status error, so not retriable by this logic.
		return false
	}
	switch st.Code() {
	case codes.Unavailable, codes.ResourceExhausted, codes.Unauthenticated:
		// Unauthenticated is included because it can result from a transient
		// failure to fetch an auth token due to network issues (like unexpected EOF).
		return true
	default:
		return false
	}
}

// REFACTOR: This new helper wraps any function in a retry loop, making
// test setup operations (like resource creation) resilient to network errors.
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
			t.Logf("Operation '%s' failed with a transient error, retrying (attempt %d/%d)...", operationName, i+1, maxRetries)
			time.Sleep(time.Duration(i*100+50) * time.Millisecond) // backoff with jitter
			continue
		}

		// For non-retriable errors, fail immediately.
		require.NoError(t, err, "Operation '%s' failed with a non-retriable error", operationName)
		return
	}

	// If all retries fail, fail the test.
	require.NoError(t, lastErr, "Operation '%s' failed after all retries", operationName)
}

// TestApplyIAMPolicy_AtomicUpdate verifies that the new ApplyIAMPolicy method
// correctly overwrites the entire policy on a resource, ensuring an atomic update.
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
		finalBindingCount := 0
		for range policy.Members(gcpiam.RoleName(desiredRole)) {
			finalBindingCount++
		}

		// The final state must have the desired role, not have the rogue role, and have exactly one member in that role.
		return hasDesiredRole && !hasRogueRole && finalBindingCount == 1
	}, 2*time.Minute, 5*time.Second, "The final policy state was not what was expected.")

	t.Log("✅ Atomic update verified. Policy is in the correct final state.")
}

// REFACTOR: This helper is updated with a retry loop to handle transient
// network errors during test setup.
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

		req := &resourcemanagerpb.GetProjectRequest{
			Name: fmt.Sprintf("projects/%s", projectID),
		}

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

// TestCheckResourceIAMBinding_PubSub verifies the add, check, and remove lifecycle for Pub/Sub IAM bindings.
func TestCheckResourceIAMBinding_PubSub(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create a GoogleIAMClient instance.
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	// Create a temporary Pub/Sub topic and subscription for the test.
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
	t.Logf("Successfully created test topic: %s", topicName)
	t.Cleanup(func() {
		t.Logf("Cleaning up test topic: %s", topicName)
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer deleteCancel()
		require.NoError(t, topic.Delete(deleteCtx))
	})

	subName := fmt.Sprintf("test-iam-check-sub-%d", time.Now().UnixNano())
	var sub *pubsub.Subscription
	executeWithRetry(t, "CreatePubSubSubscription", func() error {
		var createErr error
		sub, createErr = psClient.CreateSubscription(ctx, subName, pubsub.SubscriptionConfig{Topic: topic})
		return createErr
	})

	require.NotNil(t, sub)
	t.Logf("Successfully created test subscription: %s", subName)
	t.Cleanup(func() {
		t.Logf("Cleaning up test subscription: %s", subName)
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer deleteCancel()
		require.NoError(t, sub.Delete(deleteCtx))
	})

	// Use the default compute service account as the member to grant roles to.
	member := fmt.Sprintf("serviceAccount:%s-compute@developer.gserviceaccount.com", projectNumber)
	role := "roles/pubsub.viewer"
	topicBinding := iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: topicName, Role: role}
	subBinding := iam.IAMBinding{ResourceType: "pubsub_subscription", ResourceID: subName, Role: role}

	// --- Act & Assert ---

	// 1. Initial State: Verify the binding does not exist.
	t.Log("Verifying initial state: binding should not exist...")
	found, err := iamClient.CheckResourceIAMBinding(ctx, topicBinding, member)
	require.NoError(t, err)
	require.False(t, found, "Binding should not exist initially for topic")

	found, err = iamClient.CheckResourceIAMBinding(ctx, subBinding, member)
	require.NoError(t, err)
	require.False(t, found, "Binding should not exist initially for subscription")
	t.Log("✅ Initial state verified.")

	// 2. Add Binding: Grant the role to the member.
	t.Logf("Adding role '%s' to member '%s'...", role, member)
	err = iamClient.AddResourceIAMBinding(ctx, topicBinding, member)
	require.NoError(t, err)
	err = iamClient.AddResourceIAMBinding(ctx, subBinding, member)
	require.NoError(t, err)
	t.Log("AddResourceIAMBinding completed.")

	// 3. Verify Binding Exists: Use Eventually to handle IAM propagation delay.
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

	// 4. Remove Binding: Revoke the role from the member.
	t.Logf("Removing role '%s' from member '%s'...", role, member)
	err = iamClient.RemoveResourceIAMBinding(ctx, topicBinding, member)
	require.NoError(t, err)
	err = iamClient.RemoveResourceIAMBinding(ctx, subBinding, member)
	require.NoError(t, err)
	t.Log("RemoveResourceIAMBinding completed.")

	// 5. Verify Binding is Removed: Use Eventually to handle propagation delay.
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

// TestAddResourceIAMBinding_Concurrent specifically tests for the "get-modify-set" race condition.
// It applies two different roles to the same resource concurrently and verifies both are present.
func TestAddResourceIAMBinding_Concurrent(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create a temporary Pub/Sub topic for the test.
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	topicName := fmt.Sprintf("test-iam-race-topic-%d", time.Now().UnixNano())
	var topic *pubsub.Topic
	executeWithRetry(t, "CreatePubSubTopic", func() error {
		var createErr error
		topic, createErr = psClient.CreateTopic(ctx, topicName)
		return createErr
	})
	require.NotNil(t, topic)
	t.Logf("Successfully created test topic: %s", topicName)
	t.Cleanup(func() {
		t.Logf("Cleaning up test topic: %s", topicName)
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer deleteCancel()
		require.NoError(t, topic.Delete(deleteCtx))
	})

	// Use the default compute service account as the member to grant roles to.
	// This avoids creating/deleting SAs for this specific test.
	member := fmt.Sprintf("serviceAccount:%s-compute@developer.gserviceaccount.com", projectNumber)
	role1 := "roles/pubsub.viewer"
	role2 := "roles/pubsub.editor"

	binding1 := iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: topicName, Role: role1}
	binding2 := iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: topicName, Role: role2}

	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	// --- Act ---
	t.Logf("Applying two roles ('%s', '%s') to topic '%s' concurrently...", role1, role2, topicName)
	var wg sync.WaitGroup
	errChan := make(chan error, 2)
	wg.Add(2)

	go func() {
		defer wg.Done()
		errChan <- iamClient.AddResourceIAMBinding(ctx, binding1, member)
	}()
	go func() {
		defer wg.Done()
		errChan <- iamClient.AddResourceIAMBinding(ctx, binding2, member)
	}()

	wg.Wait()
	close(errChan)

	for err := range errChan {
		require.NoError(t, err, "AddResourceIAMBinding failed")
	}
	t.Log("Both concurrent AddResourceIAMBinding calls completed without error.")

	// --- Assert ---
	t.Log("Verifying that both roles are present on the final IAM policy...")
	require.Eventually(t, func() bool {
		policy, err := topic.IAM().Policy(ctx)
		if err != nil {
			t.Logf("Verification attempt failed to get IAM policy: %v", err)
			return false
		}
		// FIX: The role must be cast to gcpiam.RoleName using the aliased import.
		hasRole1 := policy.HasRole(member, gcpiam.RoleName(role1))
		hasRole2 := policy.HasRole(member, gcpiam.RoleName(role2))
		if !hasRole1 {
			t.Logf("Role 1 ('%s') is missing from policy", role1)
		}
		if !hasRole2 {
			t.Logf("Role 2 ('%s') is missing from policy", role2)
		}
		return hasRole1 && hasRole2
	}, 2*time.Minute, 5*time.Second, "IAM policy did not contain both roles after concurrent updates")

	t.Log("✅ Verification successful: Both roles are present.")
}

// TestCheckResourceIAMBinding_BigQuery verifies the add, check, and remove lifecycle for BigQuery dataset-level IAM bindings.
func TestCheckResourceIAMBinding_BigQuery(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create clients for IAM and BigQuery.
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	bqClient, err := bigquery.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bqClient.Close() })

	// Create a temporary BigQuery dataset for the test.
	datasetID := fmt.Sprintf("test_iam_check_dataset_%d", time.Now().UnixNano())
	dataset := bqClient.Dataset(datasetID)
	executeWithRetry(t, "CreateBigQueryDataset", func() error {
		return dataset.Create(ctx, &bigquery.DatasetMetadata{Location: "US"})
	})
	t.Logf("Successfully created test BigQuery dataset: %s", datasetID)
	t.Cleanup(func() {
		t.Logf("Cleaning up test BigQuery dataset: %s", datasetID)
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer deleteCancel()
		// Delete the dataset and all its contents.
		require.NoError(t, dataset.DeleteWithContents(deleteCtx))
	})

	// Define the member, role, and binding to test.
	member := fmt.Sprintf("serviceAccount:%s-compute@developer.gserviceaccount.com", projectNumber)
	role := "roles/bigquery.dataViewer"
	binding := iam.IAMBinding{ResourceType: "bigquery_dataset", ResourceID: datasetID, Role: role}

	// --- Act & Assert ---

	// 1. Initial State: Verify the binding does not exist.
	t.Log("Verifying initial state: binding should not exist...")
	found, err := iamClient.CheckResourceIAMBinding(ctx, binding, member)
	require.NoError(t, err)
	require.False(t, found, "Binding should not exist initially for dataset")
	t.Log("✅ Initial state verified.")

	// 2. Add Binding: Grant the role to the member.
	t.Logf("Adding role '%s' to member '%s'...", role, member)
	err = iamClient.AddResourceIAMBinding(ctx, binding, member)
	require.NoError(t, err)
	t.Log("AddResourceIAMBinding completed.")

	// 3. Verify Binding Exists: Use Eventually to handle IAM propagation delay.
	t.Log("Verifying binding existence (polling)...")
	require.Eventually(t, func() bool {
		found, err = iamClient.CheckResourceIAMBinding(ctx, binding, member)
		return err == nil && found
	}, 2*time.Minute, 5*time.Second, "Binding for dataset did not propagate in time")
	t.Log("✅ Binding existence verified.")

	// 4. Remove Binding: Revoke the role from the member.
	t.Logf("Removing role '%s' from member '%s'...", role, member)
	err = iamClient.RemoveResourceIAMBinding(ctx, binding, member)
	require.NoError(t, err)
	t.Log("RemoveResourceIAMBinding completed.")

	// 5. Verify Binding is Removed: Use Eventually to handle propagation delay.
	t.Log("Verifying binding removal (polling)...")
	require.Eventually(t, func() bool {
		found, err = iamClient.CheckResourceIAMBinding(ctx, binding, member)
		return err == nil && !found
	}, 2*time.Minute, 5*time.Second, "Binding for dataset was not removed in time")
	t.Log("✅ Binding removal verified.")
}

// TestCheckResourceIAMBinding_BigQueryTable verifies the add, check, and remove lifecycle for BigQuery table-level IAM bindings.
func TestCheckResourceIAMBinding_BigQueryTable(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create clients for IAM and BigQuery.
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	bqClient, err := bigquery.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bqClient.Close() })

	// Create a temporary BigQuery dataset and table for the test.
	datasetID := fmt.Sprintf("test_iam_table_dataset_%d", time.Now().UnixNano())
	dataset := bqClient.Dataset(datasetID)
	executeWithRetry(t, "CreateBigQueryDatasetForTableTest", func() error {
		return dataset.Create(ctx, &bigquery.DatasetMetadata{Location: "US"})
	})
	t.Logf("Successfully created test BigQuery dataset: %s", datasetID)
	t.Cleanup(func() {
		t.Logf("Cleaning up test BigQuery dataset: %s", datasetID)
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer deleteCancel()
		require.NoError(t, dataset.DeleteWithContents(deleteCtx))
	})

	tableID := fmt.Sprintf("test_iam_check_table_%d", time.Now().UnixNano())
	table := dataset.Table(tableID)
	schema := bigquery.Schema{
		{Name: "id", Type: bigquery.StringFieldType, Required: true},
		{Name: "data", Type: bigquery.StringFieldType},
	}
	executeWithRetry(t, "CreateBigQueryTable", func() error {
		return table.Create(ctx, &bigquery.TableMetadata{Schema: schema})
	})
	t.Logf("Successfully created test BigQuery table: %s", tableID)
	// Table is deleted when dataset is deleted with contents.

	// Define the member, role, and binding to test.
	member := fmt.Sprintf("serviceAccount:%s-compute@developer.gserviceaccount.com", projectNumber)
	role := "roles/bigquery.dataViewer"
	binding := iam.IAMBinding{
		ResourceType: "bigquery_table",
		ResourceID:   fmt.Sprintf("%s:%s", datasetID, tableID),
		Role:         role,
	}

	// --- Act & Assert ---

	// 1. Initial State: Verify the binding does not exist.
	t.Log("Verifying initial state for table binding...")
	found, err := iamClient.CheckResourceIAMBinding(ctx, binding, member)
	require.NoError(t, err)
	require.False(t, found, "Binding should not exist initially for table")
	t.Log("✅ Initial state verified for table.")

	// 2. Add Binding: Grant the role to the member.
	t.Logf("Adding role '%s' to member '%s' on table...", role, member)
	err = iamClient.AddResourceIAMBinding(ctx, binding, member)
	require.NoError(t, err)
	t.Log("AddResourceIAMBinding for table completed.")

	// 3. Verify Binding Exists: Use Eventually to handle IAM propagation delay.
	t.Log("Verifying table binding existence (polling)...")
	require.Eventually(t, func() bool {
		found, err = iamClient.CheckResourceIAMBinding(ctx, binding, member)
		return err == nil && found
	}, 2*time.Minute, 5*time.Second, "Binding for table did not propagate in time")
	t.Log("✅ Table binding existence verified.")

	// 4. Remove Binding: Revoke the role from the member.
	t.Logf("Removing role '%s' from member '%s' on table...", role, member)
	err = iamClient.RemoveResourceIAMBinding(ctx, binding, member)
	require.NoError(t, err)
	t.Log("RemoveResourceIAMBinding for table completed.")

	// 5. Verify Binding is Removed: Use Eventually to handle propagation delay.
	t.Log("Verifying table binding removal (polling)...")
	require.Eventually(t, func() bool {
		found, err = iamClient.CheckResourceIAMBinding(ctx, binding, member)
		return err == nil && !found
	}, 2*time.Minute, 5*time.Second, "Binding for table was not removed in time")
	t.Log("✅ Table binding removal verified.")
}

// TestGrantCloudRunAgentPermissions verifies that the helper function can grant the
// Artifact Registry Reader role to the Cloud Run service agent.
func TestGrantCloudRunAgentPermissions(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	const location = "europe-west1"
	repoName := fmt.Sprintf("test-repo-for-iam-%d", time.Now().UnixNano())

	// Create a temporary Artifact Registry repository for the test.
	arClient, err := artifactregistry.NewClient(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = arClient.Close() })

	createOp, err := arClient.CreateRepository(ctx, &artifactregistrypb.CreateRepositoryRequest{
		Parent:       fmt.Sprintf("projects/%s/locations/%s", projectID, location),
		RepositoryId: repoName,
		Repository:   &artifactregistrypb.Repository{Format: artifactregistrypb.Repository_DOCKER},
	})
	require.NoError(t, err)
	executeWithRetry(t, "WaitForCreateRepo", func() error {
		_, waitErr := createOp.Wait(ctx)
		return waitErr
	})
	t.Logf("Successfully created test Artifact Registry repository: %s", repoName)
	t.Cleanup(func() {
		t.Logf("Cleaning up Artifact Registry repository: %s", repoName)
		deleteReq := &artifactregistrypb.DeleteRepositoryRequest{Name: fmt.Sprintf("projects/%s/locations/%s/repositories/%s", projectID, location, repoName)}
		executeWithRetry(t, "DeleteRepo", func() error {
			_, delErr := arClient.DeleteRepository(context.Background(), deleteReq)
			// Ignore NotFound errors during cleanup
			if delErr != nil && status.Code(delErr) == codes.NotFound {
				return nil
			}
			return delErr
		})
	})

	// Create the client needed by the function.
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	repoConfig := iam.RepoConfig{
		ProjectID:     projectID,
		ProjectNumber: projectNumber,
		Name:          repoName,
		Location:      location,
	}

	// --- Act ---
	t.Log("Granting permissions to Cloud Run agent...")
	modified, err := iam.GrantCloudRunAgentPermissions(ctx, repoConfig, iamClient, logger)
	require.NoError(t, err)
	require.True(t, modified, "Expected the policy to be modified")

	// --- Assert ---
	t.Log("Verifying repository IAM binding...")
	cloudRunAgentMember := fmt.Sprintf("serviceAccount:service-%s@serverless-robot-prod.iam.gserviceaccount.com", projectNumber)
	readerRole := "roles/artifactregistry.reader"
	repoResource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", projectID, location, repoName)

	require.Eventually(t, func() bool {
		policy, err := arClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: repoResource})
		if err != nil {
			t.Logf("Attempt failed to get repository IAM policy: %v", err)
			return false
		}
		for _, binding := range policy.Bindings {
			if binding.Role == readerRole {
				for _, member := range binding.Members {
					if member == cloudRunAgentMember {
						t.Log("✅ Verification successful: Found Cloud Run agent in reader role.")
						return true
					}
				}
			}
		}
		t.Log("Attempt failed: Cloud Run agent not yet found in reader role.")
		return false
	}, 60*time.Second, 5*time.Second, "Artifact Registry IAM policy did not propagate in time")
}
