//go:build integration

package iam_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/pubsub"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
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
		fullSvcName := fmt.Sprintf("projects/%s/locations/%s/services/%s", projectID, location, serviceName)
		t.Logf("Cleaning up Cloud Run service: %s", serviceName)
		deleteOp, err := runService.Projects.Locations.Services.Delete(fullSvcName).Do()
		if err != nil {
			t.Logf("Failed to initiate deletion of Cloud Run service %s: %v", serviceName, err)
			return
		}
		_, err = runService.Projects.Locations.Operations.Wait(deleteOp.Name, &run.GoogleLongrunningWaitOperationRequest{}).Do()
		if err != nil {
			t.Logf("Failed to wait for deletion of Cloud Run service %s: %v", serviceName, err)
		}
	})
}

func TestIAMManager_FullFlow(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	logger := zerolog.New(os.Stderr).With().Timestamp().Str("test", "TestIAMManager_FullFlow").Logger()

	runID := time.Now().UnixNano()
	testTopicName := fmt.Sprintf("test-topic-for-iam-%d", runID)
	testSecretName := fmt.Sprintf("test-secret-for-iam-%d", runID)
	testDatasetName := fmt.Sprintf("test_dataset_for_iam_%d", runID) // BQ names need underscores
	testSaName := "pooled-iam-test-sa"
	testSaPrefix := "it-iam"
	targetServiceName := fmt.Sprintf("target-service-%d", runID)
	const testLocation = "europe-west1"

	// REFACTOR: Added BigQuery dataset with a primitive WRITER role to the architecture.
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Region: testLocation},
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
							Region: testLocation,
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
					BigQueryDatasets: []servicemanager.BigQueryDataset{
						{
							CloudResource: servicemanager.CloudResource{
								Name: testDatasetName,
								IAMPolicy: []servicemanager.IAM{
									// Note: For dataset ACLs, we use primitive roles.
									// "WRITER" corresponds to roles/bigquery.dataEditor.
									{Name: "test-service", Role: "WRITER"},
								},
							},
						},
					},
				},
			},
		},
	}

	iamClient, err := iam.NewTestIAMClient(ctx, projectID, logger, testSaPrefix)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	iamManager, err := iam.NewIAMManager(iamClient, logger)
	require.NoError(t, err)

	// 2. Ensure prerequisite resources exist.
	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })
	topic, err := psClient.CreateTopic(ctx, testTopicName)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := topic.Delete(context.Background()); err != nil {
			t.Logf("Failed to delete topic %s: %v", testTopicName, err)
		}
	})

	secretClient, err := secretmanager.NewClient(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = secretClient.Close() })
	secret, err := servicemanager.EnsureSecretExistsWithValue(ctx, secretClient, projectID, testSecretName, "test-data")
	require.NoError(t, err)
	t.Cleanup(func() {
		secretPath := fmt.Sprintf("projects/%s/secrets/%s", projectID, testSecretName)
		req := &secretmanagerpb.DeleteSecretRequest{Name: secretPath}
		if err := secretClient.DeleteSecret(context.Background(), req); err != nil {
			t.Logf("Failed to delete secret %s: %v", testSecretName, err)
		}
	})

	// REFACTOR: Create the BigQuery dataset needed for the test.
	bqClient, err := bigquery.NewClient(ctx, projectID)
	require.NoError(t, err)
	err = bqClient.Dataset(testDatasetName).Create(ctx, &bigquery.DatasetMetadata{Location: "EU"})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := bqClient.Dataset(testDatasetName).Delete(context.Background()); err != nil {
			t.Logf("Failed to delete dataset %s: %v", testDatasetName, err)
		}
	})

	ensureTestCloudRunService(t, ctx, projectID, testLocation, targetServiceName)

	// --- Act ---
	t.Log("Applying IAM policies for the dataflow...")
	err = iamManager.ApplyIAMForService(ctx, arch, "test-dataflow", "test-service")
	require.NoError(t, err)

	// --- Assert ---
	t.Log("Verifying the IAM policies were set correctly...")
	require.Eventually(t, func() bool {
		// Verify Pub/Sub policy
		topicPolicy, err := topic.IAM().Policy(ctx)
		if err != nil {
			t.Logf("Attempt failed to get topic IAM policy: %v", err)
			return false
		}
		foundPubSubBinding := false
		for _, member := range topicPolicy.Members("roles/pubsub.publisher") {
			if strings.HasPrefix(member, "serviceAccount:"+testSaPrefix) {
				foundPubSubBinding = true
				break
			}
		}
		if !foundPubSubBinding {
			t.Log("Attempt failed: Pub/Sub binding not found.")
		}

		// Verify Secret Manager policy
		secretName := strings.Split(secret.Name, "/versions/")[0]
		secretPolicy, err := secretClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: secretName})
		if err != nil {
			t.Logf("Attempt failed to get secret IAM policy: %v", err)
			return false
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
		if !foundSecretBinding {
			t.Log("Attempt failed: Secret binding not found.")
		}

		// Verify Cloud Run policy
		runService, err := run.NewService(ctx)
		if err != nil {
			t.Logf("Attempt failed to create Cloud Run client: %v", err)
			return false
		}
		fullSvcName := fmt.Sprintf("projects/%s/locations/%s/services/%s", projectID, testLocation, targetServiceName)
		runPolicy, err := runService.Projects.Locations.Services.GetIamPolicy(fullSvcName).Do()
		if err != nil {
			t.Logf("Attempt failed to get Cloud Run IAM policy: %v", err)
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
		if !foundRunBinding {
			t.Log("Attempt failed: Cloud Run binding not found.")
		}

		// REFACTOR: Verify BigQuery dataset policy
		foundBQBinding := false
		meta, err := bqClient.Dataset(testDatasetName).Metadata(ctx)
		if err != nil {
			t.Logf("Attempt failed to get BQ dataset metadata: %v", err)
			return false
		}
		for _, entry := range meta.Access {
			if entry.Role == bigquery.WriterRole && strings.Contains(entry.Entity, testSaPrefix) {
				foundBQBinding = true
				break
			}
		}
		if !foundBQBinding {
			t.Log("Attempt failed: BigQuery binding not found.")
		}

		return foundPubSubBinding && foundSecretBinding && foundRunBinding && foundBQBinding
	}, 60*time.Second, 5*time.Second, "IAM policies did not propagate in time")

	t.Log("✅ Verification successful.")
}
