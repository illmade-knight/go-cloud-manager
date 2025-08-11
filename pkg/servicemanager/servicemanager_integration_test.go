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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	projectID := "sm-it-project"
	runID := uuid.New().String()[:8]
	df1Topic := fmt.Sprintf("df1-topic-%s", runID)
	df1Bucket := fmt.Sprintf("df1-bucket-%s", runID)
	df1Firestore := fmt.Sprintf("df1-db-%s", runID)
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
					// NEW_CODE: Added a Firestore database to the ephemeral dataflow.
					FirestoreDatabases: []servicemanager.FirestoreDatabase{{
						CloudResource: servicemanager.CloudResource{Name: df1Firestore},
						LocationID:    "nam5",
						Type:          servicemanager.FirestoreModeNative,
					}},
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
	// GCS
	gcsConfig := emulators.GetDefaultGCSConfig(projectID, "")
	gcsConnection := emulators.SetupGCSEmulator(t, ctx, gcsConfig)
	gcsClient, err := servicemanager.CreateGoogleGCSClient(ctx, gcsConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = gcsClient.Close() })

	// Pub/Sub
	psConnection := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID, nil))
	psClient, err := servicemanager.CreateGoogleMessagingClient(ctx, projectID, psConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	// BigQuery
	bqConnection := emulators.SetupBigQueryEmulator(t, ctx, emulators.GetDefaultBigQueryConfig(projectID, nil, nil))
	bqClient, err := servicemanager.CreateGoogleBigQueryClient(ctx, projectID, bqConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bqClient.Close() })

	// NEW_CODE: Setup Firestore emulator and its client.
	fsConfig := emulators.GetDefaultFirestoreConfig(projectID)
	fsConnection := emulators.SetupFirestoreEmulator(t, ctx, fsConfig)
	fsTestAdapter := &firestoreEmulatorClientAdapter{
		projectID: projectID,
		opts:      fsConnection.ClientOptions,
	}

	logger := zerolog.New(zerolog.NewConsoleWriter())
	// NEW_CODE: Pass the new Firestore test adapter to the ServiceManager constructor.
	sm, err := servicemanager.NewServiceManagerFromClients(psClient, gcsClient, bqClient, fsTestAdapter, arch.Environment, nil, logger)
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
		assert.Len(t, provisioned.FirestoreDatabases, 1) // NEW_CODE: Verify Firestore DB was provisioned.
		t.Log("--- SetupAll finished successfully ---")

		t.Log("--- Verifying resource existence in emulators ---")
		err = sm.VerifyDataflow(ctx, arch, "telemetry-pipeline")
		assert.NoError(t, err, "Ephemeral dataflow resources should exist after setup")
		err = sm.VerifyDataflow(ctx, arch, "reporting-pipeline")
		assert.NoError(t, err, "Permanent dataflow resources should exist after setup")

		t.Log("--- Verification successful, all resources exist ---")
	})

	t.Run("Teardown and Verify Ephemeral", func(t *testing.T) {
		t.Log("--- Starting TeardownAll ---")
		teardownErr := sm.TeardownAll(ctx, arch)
		require.NoError(t, teardownErr, "TeardownAll should not fail")
		t.Log("--- TeardownAll finished successfully ---")

		t.Log("--- Verifying resource state after teardown ---")
		// Verify Dataflow 1 (ephemeral) resources are DELETED (or skipped in Firestore's case)
		err := sm.VerifyDataflow(ctx, arch, "telemetry-pipeline")
		// Expect an error because GCS and Pub/Sub resources were deleted.
		// The Firestore verification will pass because its teardown is a no-op.
		assert.Error(t, err, "Ephemeral dataflow verification should fail after teardown")
		assert.Contains(t, err.Error(), "bucket 'df1-bucket-s' not found", "Expected GCS bucket to be deleted") // Check a substring of the bucket name
		assert.Contains(t, err.Error(), "topic 'df1-topic' not found", "Expected Topic to be deleted")
		assert.NotContains(t, err.Error(), "firestore", "Firestore verification should not fail")

		// Verify Dataflow 2 (permanent) resources STILL EXIST
		err = sm.VerifyDataflow(ctx, arch, "reporting-pipeline")
		assert.NoError(t, err, "Permanent BigQuery dataset SHOULD STILL exist after teardown")
		t.Log("--- Deletion verification successful ---")
	})
}
