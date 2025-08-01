//go:build integration

package servicemanager_test

import (
	"cloud.google.com/go/bigquery"
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// TestBigQueryManager_Integration tests the manager's full lifecycle (Create, Verify, Teardown)
// against a live BigQuery emulator.
func TestBigQueryManager_Integration(t *testing.T) {
	// --- ARRANGE: Set up context, configuration, and clients ---
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "bq-it-project"
	runID := uuid.New().String()[:8]
	datasetName := fmt.Sprintf("it_dataset_%s", runID)
	tableName := fmt.Sprintf("it_table_%s", runID)

	// Define the resources to be managed.
	resources := servicemanager.CloudResourcesSpec{
		BigQueryDatasets: []servicemanager.BigQueryDataset{
			{CloudResource: servicemanager.CloudResource{Name: datasetName}},
		},
		BigQueryTables: []servicemanager.BigQueryTable{
			{
				CloudResource: servicemanager.CloudResource{Name: tableName},
				Dataset:       datasetName,
				SchemaType:    "BQTestSchema", // This schema must be registered.
			},
		},
	}

	// Set up the BigQuery emulator.
	t.Log("Setting up BigQuery emulator...")
	bqConnection := emulators.SetupBigQueryEmulator(t, ctx, emulators.GetDefaultBigQueryConfig(projectID, nil, nil))

	// Create a single BigQuery client instance configured to connect to the emulator.
	bqClient, err := bigquery.NewClient(ctx, projectID, bqConnection.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() {
		closeErr := bqClient.Close()
		if closeErr != nil {
			t.Logf("Failed to close BigQuery client: %v", closeErr)
		}
	})

	// Register the schema type used in the test resources.
	servicemanager.RegisterSchema("BQTestSchema", MonitorReadings{})

	// Create the BigQueryManager instance, wrapping the emulator client in our adapter.
	logger := zerolog.New(zerolog.NewConsoleWriter())
	environment := servicemanager.Environment{ProjectID: projectID, Location: "US"}
	bqAdapter := servicemanager.NewAdapter(bqClient)
	manager, err := servicemanager.NewBigQueryManager(bqAdapter, logger, environment)
	require.NoError(t, err)

	// --- ACT & ASSERT: Execute and verify each lifecycle phase ---

	// Phase 1: CREATE Resources
	t.Run("CreateResources", func(t *testing.T) {
		t.Log("--- Starting CreateResources ---")
		provTables, provDatasets, createErr := manager.CreateResources(ctx, resources)
		require.NoError(t, createErr)
		assert.Len(t, provDatasets, 1, "Should provision one dataset")
		assert.Len(t, provTables, 1, "Should provision one table")
		t.Log("--- CreateResources finished successfully ---")
	})

	// Phase 2: VERIFY Resources Exist
	t.Run("VerifyResourcesExist", func(t *testing.T) {
		t.Log("--- Verifying resources exist in emulator ---")
		// Verify directly using the real client to ensure resources were actually created.
		_, metadataErr := bqClient.Dataset(datasetName).Metadata(ctx)
		assert.NoError(t, metadataErr, "BigQuery dataset should exist after creation")

		_, tableMetadataErr := bqClient.Dataset(datasetName).Table(tableName).Metadata(ctx)
		assert.NoError(t, tableMetadataErr, "BigQuery table should exist after creation")
		t.Log("--- Verification successful, all resources exist ---")
	})

	// Phase 3: TEARDOWN Resources
	t.Run("TeardownResources", func(t *testing.T) {
		t.Log("--- Starting Teardown ---")
		teardownErr := manager.Teardown(ctx, resources)
		require.NoError(t, teardownErr, "Teardown should not fail")
		t.Log("--- Teardown finished successfully ---")
	})

	// Phase 4: VERIFY Resources are Deleted
	t.Run("VerifyResourcesDeleted", func(t *testing.T) {
		t.Log("--- Verifying resources are deleted from emulator ---")
		_, metadataErr := bqClient.Dataset(datasetName).Metadata(ctx)
		assert.Error(t, metadataErr, "BigQuery dataset should NOT exist after teardown")
		t.Log("--- Deletion verification successful ---")
	})
}
