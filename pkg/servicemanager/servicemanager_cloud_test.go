//go:build cloud_integration

package servicemanager_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type GardenMonitorReadings struct{}

func init() {
	// Ensure the schema is registered for the cloud test.
	servicemanager.RegisterSchema("CloudTestSchema", GardenMonitorReadings{})
}

// TestServiceManager_RealIntegration_FullLifecycle tests the top-level ServiceManager's ability
// to set up and tear down an entire architecture against REAL Google Cloud services.
func TestServiceManager_RealIntegration_FullLifecycle(t *testing.T) {
	// --- ARRANGE: Set up context, configuration, and clients ---
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectID := auth.CheckGCPAuth(t)
	runID := uuid.New().String()
	df1Topic := fmt.Sprintf("df1-topic-%s", runID[:8])
	df1Bucket := fmt.Sprintf("sm-it-df1-bucket-%s", strings.ReplaceAll(runID, "-", ""))
	df2Dataset := fmt.Sprintf("df2_dataset_%s", runID[:8])
	df2Table := fmt.Sprintf("df2_table_%s", runID[:8])

	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			Name:      "real-integration",
			ProjectID: projectID,
			Location:  "US",
		},
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
						SchemaType:    "CloudTestSchema",
					}},
				},
			},
		},
	}

	logger := zerolog.New(zerolog.NewConsoleWriter())
	sm, err := servicemanager.NewServiceManager(ctx, arch, nil, logger)
	require.NoError(t, err)

	// Final cleanup of the permanent resource, ensuring it happens even if the test fails.
	t.Cleanup(func() {
		t.Log("--- Performing final cleanup of permanent resources ---")
		bqClient, err := bigquery.NewClient(ctx, projectID)
		if err == nil {
			defer func() {
				_ = bqClient.Close()
			}()

			err = bqClient.Dataset(df2Dataset).DeleteWithContents(ctx)
			if err != nil {
				t.Logf("Final cleanup of permanent BQ dataset failed: %v", err)
			}
		}
	})

	// --- ACT & ASSERT: Execute and verify each lifecycle phase ---

	t.Run("Setup and Verify All", func(t *testing.T) {
		t.Logf("--- Starting SetupAll in project %s ---", projectID)
		provisioned, setupErr := sm.SetupAll(ctx, arch)
		require.NoError(t, setupErr)
		require.NotNil(t, provisioned)
		assert.Len(t, provisioned.Topics, 1)
		assert.Len(t, provisioned.GCSBuckets, 1)
		assert.Len(t, provisioned.BigQueryDatasets, 1)
		assert.Len(t, provisioned.BigQueryTables, 1)
		t.Log("--- SetupAll finished successfully ---")

		t.Log("--- Verifying resource existence in Google Cloud ---")
		gcsClient, err := storage.NewClient(ctx)
		require.NoError(t, err)
		defer func() {
			_ = gcsClient.Close()
		}()
		_, err = gcsClient.Bucket(df1Bucket).Attrs(ctx)
		assert.NoError(t, err, "Ephemeral GCS bucket should exist after setup")

		psClient, err := pubsub.NewClient(ctx, projectID)
		require.NoError(t, err)
		defer func() {
			_ = psClient.Close()
		}()
		topicExists, err := psClient.Topic(df1Topic).Exists(ctx)
		assert.NoError(t, err)
		assert.True(t, topicExists, "Ephemeral Pub/Sub topic should exist after setup")

		bqClient, err := bigquery.NewClient(ctx, projectID)
		require.NoError(t, err)
		defer func() {
			_ = bqClient.Close()
		}()
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
		gcsClient, err := storage.NewClient(ctx)
		require.NoError(t, err)
		defer func() {
			_ = gcsClient.Close()
		}()
		_, err = gcsClient.Bucket(df1Bucket).Attrs(ctx)
		assert.ErrorIs(t, err, storage.ErrBucketNotExist, "Ephemeral GCS bucket should NOT exist after teardown")

		psClient, err := pubsub.NewClient(ctx, projectID)
		require.NoError(t, err)
		defer func() {
			_ = psClient.Close()
		}()
		topicExists, err := psClient.Topic(df1Topic).Exists(ctx)
		assert.NoError(t, err)
		assert.False(t, topicExists, "Ephemeral Pub/Sub topic should NOT exist after teardown")

		bqClient, err := bigquery.NewClient(ctx, projectID)
		require.NoError(t, err)
		defer func() {
			_ = bqClient.Close()
		}()
		_, err = bqClient.Dataset(df2Dataset).Metadata(ctx)
		assert.NoError(t, err, "Permanent BigQuery dataset SHOULD STILL exist after teardown")
		_, err = bqClient.Dataset(df2Dataset).Table(df2Table).Metadata(ctx)
		assert.NoError(t, err, "Permanent BigQuery table SHOULD STILL exist after teardown")
		t.Log("--- Deletion verification successful ---")
	})
}
