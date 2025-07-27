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

// TestBigQueryManager_Integration tests the manager against a BigQuery emulator.
func TestBigQueryManager_Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "bq-it-project"
	runID := uuid.New().String()[:8]

	// --- 1. Define Resource Names and Configuration ---
	datasetName := fmt.Sprintf("it_dataset_%s", runID)
	tableName := fmt.Sprintf("it_table_%s", runID)

	resources := servicemanager.CloudResourcesSpec{
		BigQueryDatasets: []servicemanager.BigQueryDataset{
			{CloudResource: servicemanager.CloudResource{Name: datasetName}},
		},
		BigQueryTables: []servicemanager.BigQueryTable{
			{
				CloudResource: servicemanager.CloudResource{Name: tableName},
				Dataset:       datasetName,
				SchemaType:    "BQTestSchema",
			},
		},
	}

	// --- 2. Setup Emulator and a SINGLE Real BigQuery Client ---
	t.Log("Setting up BigQuery emulator...")
	bqConnection := emulators.SetupBigQueryEmulator(t, ctx, emulators.GetDefaultBigQueryConfig(projectID, nil, nil))

	// CORRECTED: Create only ONE client for the entire test.
	bqClient, err := bigquery.NewClient(ctx, projectID, bqConnection.ClientOptions...)
	require.NoError(t, err)
	// This cleanup now correctly closes the single client instance.
	t.Cleanup(func() {
		err = bqClient.Close()
		if err != nil {
			t.Logf("Failed to close BigQuery client: %v", err)
		}
	})

	// --- 3. Create the BigQueryManager ---
	logger := zerolog.New(zerolog.NewConsoleWriter())
	servicemanager.RegisterSchema("BQTestSchema", MonitorReadings{})
	environment := servicemanager.Environment{ProjectID: projectID, Location: "US"}

	// CORRECTED: Use the new adapter to wrap the single client for the manager.
	// Do NOT create a second client.
	bqAdapter := servicemanager.NewAdapter(bqClient)

	manager, err := servicemanager.NewBigQueryManager(bqAdapter, logger, environment)
	require.NoError(t, err)

	// =========================================================================
	// --- Phase 1: CREATE Resources ---
	// =========================================================================
	t.Log("--- Starting CreateResources ---")
	provTables, provDatasets, err := manager.CreateResources(ctx, resources)
	require.NoError(t, err)
	assert.Len(t, provDatasets, 1, "Should provision one dataset")
	assert.Len(t, provTables, 1, "Should provision one table")
	t.Log("--- CreateResources finished successfully ---")

	// =========================================================================
	// --- Phase 2: VERIFY Resources Exist ---
	// =========================================================================
	t.Log("--- Verifying resources exist in emulator ---")
	// The test's own verification uses the real bqClient directly.
	_, err = bqClient.Dataset(datasetName).Metadata(ctx)
	assert.NoError(t, err, "BigQuery dataset should exist after creation")

	_, err = bqClient.Dataset(datasetName).Table(tableName).Metadata(ctx)
	assert.NoError(t, err, "BigQuery table should exist after creation")
	t.Log("--- Verification successful, all resources exist ---")

	// =========================================================================
	// --- Phase 3: TEARDOWN Resources ---
	// =========================================================================
	t.Log("--- Starting Teardown ---")
	err = manager.Teardown(ctx, resources)
	require.NoError(t, err, "Teardown should not fail")
	t.Log("--- Teardown finished successfully ---")

	// =========================================================================
	// --- Phase 4: VERIFY Resources are Deleted ---
	// =========================================================================
	t.Log("--- Verifying resources are deleted from emulator ---")
	_, err = bqClient.Dataset(datasetName).Metadata(ctx)
	assert.Error(t, err, "BigQuery dataset should NOT exist after teardown")
	t.Log("--- Deletion verification successful ---")
}
