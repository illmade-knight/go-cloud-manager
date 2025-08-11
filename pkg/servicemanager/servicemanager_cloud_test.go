//go:build cloud_integration

package servicemanager_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"

	firestoreadmin "cloud.google.com/go/firestore/apiv1/admin"
	firestoreadminpb "cloud.google.com/go/firestore/apiv1/admin/adminpb"

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
	// REFACTOR_NOTE: Using t.Cleanup for context cancellation is more robust.
	t.Cleanup(cancel)

	projectID := auth.CheckGCPAuth(t)
	runID := uuid.New().String()
	df1Topic := fmt.Sprintf("df1-topic-%s", runID[:8])
	// GCS bucket names have stricter rules, so we sanitize the UUID.
	df1Bucket := fmt.Sprintf("sm-it-df1-bucket-%s", strings.ReplaceAll(runID, "-", ""))
	df2Dataset := fmt.Sprintf("df2_dataset_%s", runID[:8])
	df2Table := fmt.Sprintf("df2_table_%s", runID[:8])
	df2Firestore := "default-db" // Logical name

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
					// NEW_CODE: Added a permanent Firestore DB to the architecture.
					// Location must be a valid GCP location for Firestore, e.g., "nam5" (North America)
					FirestoreDatabases: []servicemanager.FirestoreDatabase{{
						CloudResource: servicemanager.CloudResource{Name: df2Firestore},
						LocationID:    "nam5",
						Type:          servicemanager.FirestoreModeNative,
					}},
				},
			},
		},
	}

	logger := zerolog.New(zerolog.NewConsoleWriter())
	// The main ServiceManager constructor automatically detects the new Firestore resource.
	sm, err := servicemanager.NewServiceManager(ctx, arch, nil, logger)
	require.NoError(t, err)

	// Final cleanup of permanent resources, ensuring it happens even if the test fails.
	t.Cleanup(func() {
		t.Log("--- Performing final cleanup of permanent resources ---")
		// NEW_CODE: Log manual cleanup reminder for Firestore.
		t.Logf("!!! MANUAL CLEANUP REQUIRED for project %s: Please delete the Firestore database instance.", projectID)

		bqClient, err := bigquery.NewClient(ctx, projectID)
		if err == nil {
			err = bqClient.Dataset(df2Dataset).DeleteWithContents(ctx)
			if err != nil {
				t.Logf("Final cleanup of permanent BQ dataset failed: %v", err)
			}
			_ = bqClient.Close()
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
		assert.Len(t, provisioned.FirestoreDatabases, 1) // NEW_CODE: Check that Firestore was provisioned.
		t.Log("--- SetupAll finished successfully ---")

		t.Log("--- Verifying resource existence in Google Cloud ---")
		// Use the ServiceManager's own verification for simplicity and correctness.
		err = sm.VerifyDataflow(ctx, arch, "telemetry-pipeline")
		assert.NoError(t, err, "Ephemeral dataflow resources should exist after setup")
		err = sm.VerifyDataflow(ctx, arch, "reporting-pipeline")
		assert.NoError(t, err, "Permanent dataflow resources should exist after setup")

		// NEW_CODE: For good measure, do a direct check with the Firestore Admin client.
		fsAdminClient, fsErr := firestoreadmin.NewFirestoreAdminClient(ctx)
		require.NoError(t, fsErr)
		t.Cleanup(func() { _ = fsAdminClient.Close() })
		dbRequest := &firestoreadminpb.GetDatabaseRequest{Name: fmt.Sprintf("projects/%s/databases/(default)", projectID)}
		_, err = fsAdminClient.GetDatabase(ctx, dbRequest)
		assert.NoError(t, err, "Permanent Firestore database should exist after setup")
		t.Log("--- Verification successful, all resources exist ---")
	})

	t.Run("Teardown and Verify Ephemeral", func(t *testing.T) {
		t.Log("--- Starting TeardownAll ---")
		teardownErr := sm.TeardownAll(ctx, arch)
		require.NoError(t, teardownErr, "TeardownAll should not fail")
		t.Log("--- TeardownAll finished successfully ---")

		t.Log("--- Verifying resource state after teardown ---")
		// Verify ephemeral resources are gone.
		err := sm.VerifyDataflow(ctx, arch, "telemetry-pipeline")
		assert.Error(t, err, "Verification of ephemeral dataflow should fail after teardown")
		assert.Contains(t, err.Error(), "bucket")
		assert.Contains(t, err.Error(), "topic")

		// Verify permanent resources still exist.
		err = sm.VerifyDataflow(ctx, arch, "reporting-pipeline")
		assert.NoError(t, err, "Permanent dataflow resources should still exist after teardown")
		t.Log("--- Deletion verification successful ---")
	})
}
