//go:build integration

package iam_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/run/v2"
)

// ensureTestCloudRunService is a helper to create and clean up the target service for the test.
func ensureTestCloudRunService(t *testing.T, ctx context.Context, projectID, location, serviceName string) {
	t.Helper()
	runService, err := run.NewService(ctx)
	require.NoError(t, err)

	parent := fmt.Sprintf("projects/%s/locations/%s", projectID, location)

	createOp, err := runService.Projects.Locations.Services.Create(parent, &run.GoogleCloudRunV2Service{
		Template: &run.GoogleCloudRunV2RevisionTemplate{
			Containers: []*run.GoogleCloudRunV2Container{
				{Image: "us-docker.pkg.dev/cloudrun/container/hello"},
			},
		},
	}).ServiceId(serviceName).Do()
	require.NoError(t, err)

	_, err = runService.Projects.Locations.Operations.Wait(createOp.Name, &run.GoogleLongrunningWaitOperationRequest{}).Do()
	require.NoError(t, err)
	t.Logf("Successfully created dummy Cloud Run service: %s", serviceName)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		fullSvcName := fmt.Sprintf("projects/%s/locations/%s/services/%s", projectID, location, serviceName)
		t.Logf("Cleaning up Cloud Run service: %s", serviceName)
		deleteOp, err := runService.Projects.Locations.Services.Delete(fullSvcName).Context(cleanupCtx).Do()
		if err != nil {
			t.Logf("Failed to initiate deletion of Cloud Run service %s: %v", serviceName, err)
			return
		}
		_, err = runService.Projects.Locations.Operations.Wait(deleteOp.Name, &run.GoogleLongrunningWaitOperationRequest{}).Context(cleanupCtx).Do()
		if err != nil {
			t.Logf("Failed to wait for deletion of Cloud Run service %s: %v", serviceName, err)
		}
	})
}

// TestIAMManager_FullFlow performs a full, end-to-end integration test of the IAMManager.
// It creates real prerequisite resources, uses the IAM client to apply a comprehensive
// IAM plan, and then verifies that all policies were correctly applied.
func TestIAMManager_FullFlow(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	logger := zerolog.New(os.Stderr).With().Timestamp().Str("test", "TestIAMManager_FullFlow").Logger()

	runID := uuid.New().String()[:8]
	testTopicName := fmt.Sprintf("test-topic-for-iam-%s", runID)
	testSecretName := fmt.Sprintf("test-secret-for-iam-%s", runID)
	testDatasetName := fmt.Sprintf("test_dataset_for_iam_%s", runID)
	testTableName := fmt.Sprintf("test_table_for_iam_%s", runID)
	targetServiceName := fmt.Sprintf("target-service-%s", runID)
	const testLocation = "europe-west1"

	// 1. Create prerequisite resources for the test to apply IAM policies to.
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })
	topic, err := psClient.CreateTopic(ctx, testTopicName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = topic.Delete(context.Background()) })

	secretClient, err := secretmanager.NewClient(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = secretClient.Close() })
	_, err = servicemanager.EnsureSecretExistsWithValue(ctx, secretClient, projectID, testSecretName, "test-data")
	require.NoError(t, err)
	t.Cleanup(func() {
		req := &secretmanagerpb.DeleteSecretRequest{Name: fmt.Sprintf("projects/%s/secrets/%s", projectID, testSecretName)}
		_ = secretClient.DeleteSecret(context.Background(), req)
	})

	bqClient, err := bigquery.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bqClient.Close() })
	err = bqClient.Dataset(testDatasetName).Create(ctx, &bigquery.DatasetMetadata{Location: "EU"})
	require.NoError(t, err)
	err = bqClient.Dataset(testDatasetName).Table(testTableName).Create(ctx, &bigquery.TableMetadata{Location: "EU"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = bqClient.Dataset(testDatasetName).Table(testTableName).Delete(ctx)
		_ = bqClient.Dataset(testDatasetName).Delete(context.Background())
	})

	ensureTestCloudRunService(t, ctx, projectID, testLocation, targetServiceName)
	// NOTE: We assume a Firestore database already exists in the test project in NATIVE mode.
	// This test does not create it, only sets permissions for it.
	t.Cleanup(func() {
		t.Logf("!!! MANUAL CLEANUP REMINDER for project %s: Please ensure the Firestore database is removed if it was created for this test.", projectID)
	})

	// 2. Define an architecture that uses all these resources.
	testServiceSA := fmt.Sprintf("iam-test-sa-%s", runID)
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Region: testLocation},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"full-iam-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"iam-test-service": {
						Name:           "iam-test-service",
						ServiceAccount: testServiceSA,
						Dependencies:   []string{targetServiceName},
						Deployment:     &servicemanager.DeploymentSpec{SecretEnvironmentVars: []servicemanager.SecretEnvVar{{ValueFrom: testSecretName}}},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{CloudResource: servicemanager.CloudResource{Name: testTopicName},
							ProducerService: &servicemanager.ServiceMapping{Name: "iam-test-service"},
						},
					},
					BigQueryTables: []servicemanager.BigQueryTable{
						{
							CloudResource: servicemanager.CloudResource{Name: testTableName},
							Dataset:       testDatasetName,
							Producers:     []servicemanager.ServiceMapping{{Name: "iam-test-service"}},
						},
					},
					// UPDATE: Add a Firestore database with a producer to the architecture.
					FirestoreDatabases: []servicemanager.FirestoreDatabase{{
						CloudResource: servicemanager.CloudResource{Name: "default-db"},
						Producers:     []servicemanager.ServiceMapping{{Name: "iam-test-service"}},
					}},
				},
			},
		},
	}

	// 3. Initialize the IAM manager and orchestrator.
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	iamManager, err := iam.NewIAMManager(iamClient, logger)
	require.NoError(t, err)

	// UPDATE: This new block creates the SA and waits for it to propagate BEFORE
	// we try to apply policies that use it. This solves the race condition.
	t.Logf("Ensuring service account '%s' exists and has propagated...", testServiceSA)
	saEmail, err := iamClient.EnsureServiceAccountExists(ctx, testServiceSA)
	require.NoError(t, err)
	t.Cleanup(func() {
		// This will delete the SA at the very end of the test.
		_ = iamClient.DeleteServiceAccount(context.Background(), saEmail)
	})

	require.Eventually(t, func() bool {
		err := iamClient.GetServiceAccount(ctx, saEmail)
		if err != nil {
			t.Logf("SA propagation check failed: %v, retrying...", err)
		}
		return err == nil
	}, 2*time.Minute, 5*time.Second, "Service account did not propagate in time")
	t.Log("Service account has propagated.")

	// --- Act ---
	t.Log("Applying IAM policies for the dataflow...")
	appliedPolicies, err := iamManager.ApplyIAMForDataflow(ctx, arch, "full-iam-flow")
	require.NoError(t, err)
	require.NotEmpty(t, appliedPolicies)

	// --- Assert ---
	t.Log("Verifying the IAM policies were set correctly (polling for propagation)...")

	member := "serviceAccount:" + saEmail

	projectIAM, err := iam.NewGoogleIAMProjectClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = projectIAM.Close() })

	require.Eventually(t, func() bool {
		// Verify Pub/Sub policy
		hasPubSub, err := iamClient.CheckResourceIAMBinding(ctx, iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: testTopicName, Role: "roles/pubsub.publisher"}, member)
		if err != nil || !hasPubSub {
			t.Logf("Polling for Pub/Sub... Found: %v, Err: %v", hasPubSub, err)
			return false
		}

		// Verify Secret Manager policy
		hasSecret, err := iamClient.CheckResourceIAMBinding(ctx, iam.IAMBinding{ResourceType: "secret", ResourceID: testSecretName, Role: "roles/secretmanager.secretAccessor"}, member)
		if err != nil || !hasSecret {
			t.Logf("Polling for Secret... Found: %v, Err: %v", hasSecret, err)
			return false
		}

		// Verify Cloud Run policy
		hasRun, err := iamClient.CheckResourceIAMBinding(ctx, iam.IAMBinding{ResourceType: "cloudrun_service", ResourceID: targetServiceName, Role: "roles/run.invoker", ResourceLocation: testLocation}, member)
		if err != nil || !hasRun {
			t.Logf("Polling for Cloud Run... Found: %v, Err: %v", hasRun, err)
			return false
		}

		// Verify BigQuery table policy
		bqResourceID := fmt.Sprintf("%s:%s", testDatasetName, testTableName)
		hasBQ, err := iamClient.CheckResourceIAMBinding(ctx, iam.IAMBinding{ResourceType: "bigquery_table", ResourceID: bqResourceID, Role: "roles/bigquery.dataEditor"}, member)
		if err != nil || !hasBQ {
			t.Logf("Polling for BigQuery... Found: %v, Err: %v", hasBQ, err)
			return false
		}

		// UPDATE: Verify the new project-level Firestore policy.
		// Firestore in fact uses a project level IAM - and we set these in IAMOrchestrator instead of here
		//hasFirestore, err := projectIAM.CheckProjectIAMBinding(ctx, member, "roles/datastore.user")
		//if err != nil || !hasFirestore {
		//	t.Logf("Polling for Firestore... Found: %v, Err: %v", hasFirestore, err)
		//	return false
		//}

		return true // All checks passed
	}, 2*time.Minute, 5*time.Second, "IAM policies did not propagate in time")

	t.Log("✅ Verification successful. All IAM policies have propagated correctly.")
}
