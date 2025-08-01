//go:build integration

package servicemanager_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MonitorReadings is a dummy struct for schema registration in tests.
type MonitorReadings struct{}

func init() {
	// Register schemas used in the tests.
	servicemanager.RegisterSchema("SMTestSchema", MonitorReadings{})
}

// TestServiceManager_Integration_FullLifecycle tests the top-level ServiceManager's ability
// to set up and tear down an entire architecture against live emulators.
func TestServiceManager_Integration_FullLifecycle(t *testing.T) {
	// --- ARRANGE: Set up context, configuration, and clients ---
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "sm-it-project"
	runID := uuid.New().String()[:8]
	df1Topic := fmt.Sprintf("df1-topic-%s", runID)
	df1Bucket := fmt.Sprintf("df1-bucket-%s", runID)
	df2Dataset := fmt.Sprintf("df2_dataset_%s", runID)
	df2Table := fmt.Sprintf("df2_table_%s", runID)

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: projectID, Location: "us-central1"},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"telemetry-pipeline": {
				Name:      "telemetry-pipeline",
				Lifecycle: &servicemanager.LifecyclePolicy{Strategy: servicemanager.LifecycleStrategyEphemeral},
				Resources: servicemanager.CloudResourcesSpec{
					Topics:     []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: df1Topic}}},
					GCSBuckets: []servicemanager.GCSBucket{{CloudResource: servicemanager.CloudResource{Name: df1Bucket}, Location: "US"}},
				},
			},
			"reporting-pipeline": {
				Name:      "reporting-pipeline",
				Lifecycle: &servicemanager.LifecyclePolicy{Strategy: servicemanager.LifecycleStrategyPermanent},
				Resources: servicemanager.CloudResourcesSpec{
					BigQueryDatasets: []servicemanager.BigQueryDataset{{CloudResource: servicemanager.CloudResource{Name: df2Dataset}}},
					BigQueryTables: []servicemanager.BigQueryTable{{
						CloudResource: servicemanager.CloudResource{Name: df2Table},
						Dataset:       df2Dataset,
						SchemaType:    "SMTestSchema",
					}},
				},
			},
		},
	}

	t.Log("Setting up emulators...")
	gcsConfig := emulators.GetDefaultGCSConfig(projectID, "")
	gcsConnection := emulators.SetupGCSEmulator(t, ctx, gcsConfig)
	gcsClient, err := servicemanager.CreateGoogleGCSClient(ctx, gcsConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = gcsClient.Close() })

	psConnection := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := servicemanager.CreateGoogleMessagingClient(ctx, projectID, psConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	bqConnection := emulators.SetupBigQueryEmulator(t, ctx, emulators.GetDefaultBigQueryConfig(projectID, nil, nil))
	bqClient, err := servicemanager.CreateGoogleBigQueryClient(ctx, projectID, bqConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bqClient.Close() })

	logger := zerolog.New(zerolog.NewConsoleWriter())
	sm, err := servicemanager.NewServiceManagerFromClients(psClient, gcsClient, bqClient, arch.Environment, nil, logger)
	require.NoError(t, err)

	// --- ACT & ASSERT: Execute and verify each lifecycle phase ---

	t.Run("Setup and Verify All", func(t *testing.T) {
		t.Log("--- Starting SetupAll ---")
		provisioned, setupErr := sm.SetupAll(ctx, arch)
		require.NoError(t, setupErr)
		require.NotNil(t, provisioned)
		assert.Len(t, provisioned.Topics, 1)
		assert.Len(t, provisioned.GCSBuckets, 1)
		assert.Len(t, provisioned.BigQueryDatasets, 1)
		assert.Len(t, provisioned.BigQueryTables, 1)
		t.Log("--- SetupAll finished successfully ---")

		t.Log("--- Verifying resource existence in emulators ---")
		_, err = gcsClient.Bucket(df1Bucket).Attrs(ctx)
		assert.NoError(t, err, "Ephemeral GCS bucket should exist after setup")
		topicExists, err := psClient.Topic(df1Topic).Exists(ctx)
		assert.NoError(t, err)
		assert.True(t, topicExists, "Ephemeral Pub/Sub topic should exist after setup")

		_, err = bqClient.Dataset(df2Dataset).Metadata(ctx)
		assert.NoError(t, err, "Permanent BigQuery dataset should exist after setup")
		_, err = bqClient.Dataset(df2Dataset).Table(df2Table).Metadata(ctx)
		assert.NoError(t, err, "Permanent BigQuery table should exist after setup")
		t.Log("--- Verification successful, all resources exist ---")
	})

	t.Run("Teardown and Verify Ephemeral", func(t *testing.T) {
		t.Log("--- Starting TeardownAll ---")
		teardownErr := sm.TeardownAll(ctx, arch)
		require.NoError(t, teardownErr, "TeardownAll should not fail")
		t.Log("--- TeardownAll finished successfully ---")

		t.Log("--- Verifying resource state after teardown ---")
		// Verify Dataflow 1 (ephemeral) resources are DELETED
		_, err := gcsClient.Bucket(df1Bucket).Attrs(ctx)
		assert.Error(t, err, "Ephemeral GCS bucket should NOT exist after teardown")
		topicExists, err := psClient.Topic(df1Topic).Exists(ctx)
		assert.NoError(t, err)
		assert.False(t, topicExists, "Ephemeral Pub/Sub topic should NOT exist after teardown")

		// Verify Dataflow 2 (permanent) resources STILL EXIST
		_, err = bqClient.Dataset(df2Dataset).Metadata(ctx)
		assert.NoError(t, err, "Permanent BigQuery dataset SHOULD STILL exist after teardown")
		_, err = bqClient.Dataset(df2Dataset).Table(df2Table).Metadata(ctx)
		assert.NoError(t, err, "Permanent BigQuery table SHOULD STILL exist after teardown")
		t.Log("--- Deletion verification successful ---")
	})
}
