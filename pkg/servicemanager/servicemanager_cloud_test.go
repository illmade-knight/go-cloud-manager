//go:build cloud_integration

package servicemanager_test

import (
	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

type GardenMonitorReadings struct {
}

func init() {
	servicemanager.RegisterSchema("TestSchema", GardenMonitorReadings{})
}

// TestServiceManager_RealIntegration_FullLifecycle tests the top-level ServiceManager's ability
// to set up and tear down an entire architecture against REAL Google Cloud services.
func TestServiceManager_RealIntegration_FullLifecycle(t *testing.T) {
	ctx := context.Background()
	projectID := auth.CheckGCPAuth(t)

	runID := uuid.New().String()

	// --- 1. Define the full Microservice Architecture ---
	// Dataflow 1 (Ephemeral)
	df1Topic := fmt.Sprintf("df1-topic-%s", runID[:8])
	// GCS bucket names must be globally unique.
	df1Bucket := fmt.Sprintf("sm-it-df1-bucket-%s", strings.ReplaceAll(runID, "-", ""))

	// Dataflow 2 (Permanent)
	df2Dataset := fmt.Sprintf("df2_dataset_%s", runID[:8])
	df2Table := fmt.Sprintf("df2_table_%s", runID[:8])

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			Name:      "real-integration",
			ProjectID: projectID,
			Location:  "US", // BQ and GCS location
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"telemetry-pipeline": {
				Name: "telemetry-pipeline",
				Lifecycle: &servicemanager.LifecyclePolicy{
					Strategy: servicemanager.LifecycleStrategyEphemeral,
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics:     []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: df1Topic}}},
					GCSBuckets: []servicemanager.GCSBucket{{CloudResource: servicemanager.CloudResource{Name: df1Bucket}, Location: "US"}},
				},
			},
			"reporting-pipeline": {
				Name: "reporting-pipeline",
				Lifecycle: &servicemanager.LifecyclePolicy{
					Strategy: servicemanager.LifecycleStrategyPermanent,
				},
				Resources: servicemanager.CloudResourcesSpec{
					BigQueryDatasets: []servicemanager.BigQueryDataset{{CloudResource: servicemanager.CloudResource{Name: df2Dataset}}},
					BigQueryTables: []servicemanager.BigQueryTable{
						{
							CloudResource: servicemanager.CloudResource{Name: df2Table},
							Dataset:       df2Dataset,
							SchemaType:    "TestSchema",
						},
					},
				},
			},
		},
	}

	// --- 2. Setup REAL Google Cloud Clients ---
	t.Log("Setting up real Google Cloud clients...")
	gcsClient, err := storage.NewClient(ctx)
	require.NoError(t, err)
	defer gcsClient.Close()

	psClient, err := pubsub.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer psClient.Close()

	bqClient, err := bigquery.NewClient(ctx, projectID)
	require.NoError(t, err)
	defer bqClient.Close()

	// --- 3. Create the ServiceManager ---
	logger := zerolog.New(zerolog.NewConsoleWriter())

	// Use the production constructor, which creates all sub-managers internally.
	sm, err := servicemanager.NewServiceManager(ctx, arch, nil, logger)
	require.NoError(t, err)

	// --- 4. CREATE all resources for the entire architecture ---
	t.Logf("--- Starting SetupAll in project %s ---", projectID)
	provisioned, err := sm.SetupAll(ctx, arch)
	require.NoError(t, err)
	require.NotNil(t, provisioned)
	assert.Len(t, provisioned.Topics, 1)
	assert.Len(t, provisioned.GCSBuckets, 1)
	assert.Len(t, provisioned.BigQueryDatasets, 1)
	assert.Len(t, provisioned.BigQueryTables, 1)
	t.Log("--- SetupAll finished successfully ---")

	// --- 5. VERIFY that all resources from both dataflows now exist ---
	t.Log("--- Verifying resource existence in Google Cloud ---")
	// Dataflow 1 resources
	_, err = gcsClient.Bucket(df1Bucket).Attrs(ctx)
	assert.NoError(t, err, "Ephemeral GCS bucket should exist after setup")
	topicExists, err := psClient.Topic(df1Topic).Exists(ctx)
	assert.NoError(t, err)
	assert.True(t, topicExists, "Ephemeral Pub/Sub topic should exist after setup")

	// Dataflow 2 resources
	_, err = bqClient.Dataset(df2Dataset).Metadata(ctx)
	assert.NoError(t, err, "Permanent BigQuery dataset should exist after setup")
	_, err = bqClient.Dataset(df2Dataset).Table(df2Table).Metadata(ctx)
	assert.NoError(t, err, "Permanent BigQuery table should exist after setup")
	t.Log("--- Verification successful, all resources exist ---")

	// --- 6. TEARDOWN all ephemeral resources ---
	t.Log("--- Starting TeardownAll ---")
	err = sm.TeardownAll(ctx, arch)
	require.NoError(t, err, "TeardownAll should not fail")
	t.Log("--- TeardownAll finished successfully ---")

	// --- 7. VERIFY that ephemeral resources are GONE and permanent resources REMAIN ---
	t.Log("--- Verifying resource state after teardown ---")
	// Verify Dataflow 1 (ephemeral) resources are DELETED
	_, err = gcsClient.Bucket(df1Bucket).Attrs(ctx)
	assert.ErrorIs(t, err, storage.ErrBucketNotExist, "Ephemeral GCS bucket should NOT exist after teardown")
	topicExists, err = psClient.Topic(df1Topic).Exists(ctx)
	assert.NoError(t, err)
	assert.False(t, topicExists, "Ephemeral Pub/Sub topic should NOT exist after teardown")

	// Verify Dataflow 2 (permanent) resources STILL EXIST
	_, err = bqClient.Dataset(df2Dataset).Metadata(ctx)
	assert.NoError(t, err, "Permanent BigQuery dataset SHOULD STILL exist after teardown")
	_, err = bqClient.Dataset(df2Dataset).Table(df2Table).Metadata(ctx)
	assert.NoError(t, err, "Permanent BigQuery table SHOULD STILL exist after teardown")
	t.Log("--- Deletion verification successful ---")

	// --- 8. Final Cleanup of Permanent Resources ---
	t.Log("--- Performing final cleanup of permanent resources ---")
	err = bqClient.Dataset(df2Dataset).DeleteWithContents(ctx)
	assert.NoError(t, err, "Final cleanup of permanent BQ dataset should succeed")
	t.Log("--- Test finished ---")
}
