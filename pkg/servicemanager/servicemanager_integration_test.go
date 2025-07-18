//go:build integration

package servicemanager_test

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-iot/helpers/emulators"
	"github.com/illmade-knight/go-iot/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

// TestServiceManager_Integration_FullLifecycle tests the top-level ServiceManager's ability
// to set up and tear down an entire architecture with multiple dataflows against live emulators.
func TestServiceManager_Integration_FullLifecycle(t *testing.T) {
	ctx := context.Background()
	projectID := "sm-it-project"
	runID := uuid.New().String()[:8]

	// --- 1. Define the full Microservice Architecture ---
	// Dataflow 1 (Ephemeral)
	df1Topic := fmt.Sprintf("df1-topic-%s", runID)
	df1Bucket := fmt.Sprintf("df1-bucket-%s", runID)

	// Dataflow 2 (Permanent)
	df2Dataset := fmt.Sprintf("df2_dataset_%s", runID)
	df2Table := fmt.Sprintf("df2_table_%s", runID)

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			Name:      "integration",
			ProjectID: projectID,
			Location:  "us-central1",
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
							CloudResource:    servicemanager.CloudResource{Name: df2Table},
							Dataset:          df2Dataset,
							SchemaImportPath: "TestSchema",
						},
					},
				},
			},
		},
	}

	// --- 2. Setup Emulators ---
	t.Log("Setting up emulators...")
	gcsConfig := emulators.GetDefaultGCSConfig(projectID, "")
	gcsConnection := emulators.SetupGCSEmulator(t, ctx, gcsConfig)
	// This line is now corrected to include the necessary gcsConfig argument.
	//gcsClient := emulators.GetStorageClient(t, ctx, gcsConfig, gcsConnection.ClientOptions)
	gcsClient, err := servicemanager.CreateGoogleGCSClient(ctx, gcsConnection.ClientOptions...)
	require.NoError(t, err)
	defer gcsClient.Close()

	psConnection := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := servicemanager.CreateGoogleMessagingClient(ctx, projectID, psConnection.ClientOptions...)
	require.NoError(t, err)
	defer psClient.Close()

	bqConnection := emulators.SetupBigQueryEmulator(t, ctx, emulators.GetDefaultBigQueryConfig(projectID, nil, nil))
	bqClient, err := servicemanager.CreateGoogleBigQueryClient(ctx, projectID, bqConnection.ClientOptions...)
	require.NoError(t, err)
	defer bqClient.Close()

	// --- 3. Create the ServiceManager ---
	logger := zerolog.New(zerolog.NewConsoleWriter())
	schemaRegistry := map[string]interface{}{"TestSchema": types.GardenMonitorReadings{}}

	// Use the production constructor, which creates all sub-managers internally.

	// CORRECTED: Use the constructor designed for testing with pre-configured clients.
	sm, err := servicemanager.NewServiceManagerFromClients(psClient, gcsClient, bqClient, arch.Environment, schemaRegistry, nil, logger)
	require.NoError(t, err)

	// --- 4. CREATE all resources for the entire architecture ---
	t.Log("--- Starting SetupAll ---")
	provisioned, err := sm.SetupAll(ctx, arch)
	require.NoError(t, err)
	require.NotNil(t, provisioned)
	assert.Len(t, provisioned.Topics, 1, "Should provision one topic from dataflow 1")
	assert.Len(t, provisioned.GCSBuckets, 1, "Should provision one bucket from dataflow 1")
	assert.Len(t, provisioned.BigQueryDatasets, 1, "Should provision one dataset from dataflow 2")
	assert.Len(t, provisioned.BigQueryTables, 1, "Should provision one table from dataflow 2")
	t.Log("--- SetupAll finished successfully ---")

	// --- 5. VERIFY that all resources from both dataflows now exist ---
	t.Log("--- Verifying resource existence in emulators ---")
	// Dataflow 1 resource
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
	assert.Error(t, err, "Ephemeral GCS bucket should NOT exist after teardown")
	topicExists, err = psClient.Topic(df1Topic).Exists(ctx)
	assert.NoError(t, err)
	assert.False(t, topicExists, "Ephemeral Pub/Sub topic should NOT exist after teardown")

	// Verify Dataflow 2 (permanent) resources STILL EXIST
	_, err = bqClient.Dataset(df2Dataset).Metadata(ctx)
	assert.NoError(t, err, "Permanent BigQuery dataset SHOULD STILL exist after teardown")
	_, err = bqClient.Dataset(df2Dataset).Table(df2Table).Metadata(ctx)
	assert.NoError(t, err, "Permanent BigQuery table SHOULD STILL exist after teardown")
	t.Log("--- Deletion verification successful ---")
}
