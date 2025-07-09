//go:build integration

package servicemanager_test

import (
	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/pubsub"
	"context"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-iot/helpers/emulators"
	"github.com/illmade-knight/go-iot/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

// TestDataflowManager_Integration_Emulators tests the DataflowManager's full setup and teardown lifecycle
// against local GCS, Pub/Sub, and BigQuery emulators.
func TestDataflowManager_Integration_Emulators(t *testing.T) {
	ctx := context.Background()
	projectID := "df-manager-it-project"
	runID := uuid.New().String()[:8]

	// --- 1. Define Resource Names and Configuration ---
	topicName := "df-it-topic-" + runID
	subName := "df-it-sub-" + runID
	bucketName := "df-it-bucket-" + strings.ReplaceAll(runID, "-", "") // Bucket names must be DNS compliant
	datasetName := "df_it_dataset_" + runID
	tableName := "df_it_table_" + runID

	// This environment is passed to the sub-managers.
	managerEnv := servicemanager.Environment{
		Name:      "integration",
		ProjectID: projectID,
		Location:  "us-central1", // Used by BQ
	}

	dataflowSpec := servicemanager.CloudResourcesSpec{
		Topics: []servicemanager.TopicConfig{
			{CloudResource: servicemanager.CloudResource{Name: topicName}},
		},
		Subscriptions: []servicemanager.SubscriptionConfig{
			{
				CloudResource:      servicemanager.CloudResource{Name: subName},
				Topic:              topicName,
				AckDeadlineSeconds: 123,
			},
		},
		GCSBuckets: []servicemanager.GCSBucket{
			{
				CloudResource: servicemanager.CloudResource{Name: bucketName},
				Location:      "US", // Required for GCS bucket creation
			},
		},
		BigQueryDatasets: []servicemanager.BigQueryDataset{
			{CloudResource: servicemanager.CloudResource{Name: datasetName}},
		},
		BigQueryTables: []servicemanager.BigQueryTable{
			{
				CloudResource:          servicemanager.CloudResource{Name: tableName},
				Dataset:                datasetName,
				SchemaSourceIdentifier: "GardenMonitorReadings",
			},
		},
	}

	// --- 2. Setup Emulators and Real Clients ---
	t.Log("Setting up emulators...")
	gcsConfig := emulators.GetDefaultGCSConfig(projectID, "")
	gcsConnection := emulators.SetupGCSEmulator(t, ctx, gcsConfig)
	// This line is now corrected to include the necessary gcsConfig argument.
	gcsClient := emulators.GetStorageClient(t, ctx, gcsConfig, gcsConnection.ClientOptions)
	defer gcsClient.Close()

	psConnection := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := pubsub.NewClient(ctx, projectID, psConnection.ClientOptions...)
	require.NoError(t, err)
	defer psClient.Close()

	bqConnection := emulators.SetupBigQueryEmulator(t, ctx, emulators.GetDefaultBigQueryConfig(projectID, nil, nil))
	bqClient, err := bigquery.NewClient(ctx, projectID, bqConnection.ClientOptions...)
	require.NoError(t, err)
	defer bqClient.Close()

	// --- 3. Create Real Managers with Emulator-Connected Clients ---
	logger := zerolog.New(zerolog.NewConsoleWriter())
	schemaRegistry := map[string]interface{}{
		"GardenMonitorReadings": types.GardenMonitorReadings{},
	}

	storageManager, err := servicemanager.NewStorageManager(servicemanager.NewGCSClientAdapter(gcsClient), logger, managerEnv)
	require.NoError(t, err)

	messagingManager, err := servicemanager.NewMessagingManager(servicemanager.MessagingClientFromPubsubClient(psClient), logger, managerEnv)
	require.NoError(t, err)

	bqAdapter, err := servicemanager.CreateGoogleBigQueryClient(ctx, managerEnv.ProjectID, bqConnection.ClientOptions...)
	require.NoError(t, err)
	bigqueryManager, err := servicemanager.NewBigQueryManager(bqAdapter, logger, schemaRegistry, managerEnv)
	require.NoError(t, err)

	// --- 4. Create the DataflowManager using the real managers ---
	dfm, err := servicemanager.NewDataflowManagerFromManagers(messagingManager, storageManager, bigqueryManager, managerEnv, logger)
	require.NoError(t, err)

	// =========================================================================
	// --- Phase 1: CREATE Resources ---
	// =========================================================================
	t.Log("--- Starting CreateResources ---")
	provisioned, err := dfm.CreateResources(ctx, dataflowSpec)
	require.NoError(t, err)
	require.NotNil(t, provisioned)
	assert.Len(t, provisioned.Topics, 1, "Should provision one topic")
	assert.Len(t, provisioned.Subscriptions, 1, "Should provision one subscription")
	assert.Len(t, provisioned.GCSBuckets, 1, "Should provision one GCS bucket")
	assert.Len(t, provisioned.BigQueryDatasets, 1, "Should provision one BQ dataset")
	assert.Len(t, provisioned.BigQueryTables, 1, "Should provision one BQ table")
	t.Log("--- CreateResources finished successfully ---")

	// =========================================================================
	// --- Phase 2: VERIFY Resources Exist ---
	// =========================================================================
	t.Log("--- Verifying resources exist in emulators ---")
	// Verify GCS Bucket
	_, err = gcsClient.Bucket(bucketName).Attrs(ctx)
	assert.NoError(t, err, "GCS bucket should exist")

	// Verify Pub/Sub Topic
	topic := psClient.Topic(topicName)
	exists, err := topic.Exists(ctx)
	assert.NoError(t, err)
	assert.True(t, exists, "Pub/Sub topic should exist")

	// Verify Pub/Sub Subscription
	sub := psClient.Subscription(subName)
	exists, err = sub.Exists(ctx)
	assert.NoError(t, err)
	assert.True(t, exists, "Pub/Sub subscription should exist")
	// Also check if a specific config setting was applied
	subCfg, err := sub.Config(ctx)
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(123)*time.Second, subCfg.AckDeadline, "Subscription AckDeadline should be set correctly")

	// Verify BigQuery Dataset
	_, err = bqClient.Dataset(datasetName).Metadata(ctx)
	assert.NoError(t, err, "BigQuery dataset should exist")

	// Verify BigQuery Table
	_, err = bqClient.Dataset(datasetName).Table(tableName).Metadata(ctx)
	assert.NoError(t, err, "BigQuery table should exist")
	t.Log("--- Verification successful, all resources exist ---")

	// =========================================================================
	// --- Phase 3: TEARDOWN Resources ---
	// =========================================================================
	t.Log("--- Starting TeardownResources ---")
	err = dfm.TeardownResources(ctx, dataflowSpec)
	require.NoError(t, err, "Teardown should not fail")
	t.Log("--- TeardownResources finished successfully ---")

	// =========================================================================
	// --- Phase 4: VERIFY Resources are Deleted ---
	// =========================================================================
	t.Log("--- Verifying resources are deleted from emulators ---")
	// Verify GCS Bucket is gone
	_, err = gcsClient.Bucket(bucketName).Attrs(ctx)
	assert.Error(t, err, "GCS bucket should NOT exist after teardown")

	// Verify Pub/Sub Topic is gone
	exists, err = psClient.Topic(topicName).Exists(ctx)
	assert.NoError(t, err)
	assert.False(t, exists, "Pub/Sub topic should NOT exist after teardown")

	// Verify Pub/Sub Subscription is gone
	exists, err = psClient.Subscription(subName).Exists(ctx)
	assert.NoError(t, err)
	assert.False(t, exists, "Pub/Sub subscription should NOT exist after teardown")

	// Verify BigQuery Dataset is gone
	_, err = bqClient.Dataset(datasetName).Metadata(ctx)
	assert.Error(t, err, "BigQuery dataset should NOT exist after teardown")
	t.Log("--- Deletion verification successful ---")
}
