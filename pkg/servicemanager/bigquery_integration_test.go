//go:build integration

package servicemanager_test

import (
	"cloud.google.com/go/bigquery"
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

// TestBigQueryManager_Integration tests the manager against a live BigQuery emulator.
func TestBigQueryManager_Integration(t *testing.T) {
	ctx := context.Background()
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
				CloudResource:    servicemanager.CloudResource{Name: tableName},
				Dataset:          datasetName,
				SchemaImportPath: "TestSchema",
			},
		},
	}

	// --- 2. Setup Emulator and Real BigQuery Client ---
	t.Log("Setting up BigQuery emulator...")
	bqConnection := emulators.SetupBigQueryEmulator(t, ctx, emulators.GetDefaultBigQueryConfig(projectID, nil, nil))
	bqClient, err := bigquery.NewClient(ctx, projectID, bqConnection.ClientOptions...)
	require.NoError(t, err)
	defer bqClient.Close()

	// --- 3. Create the BigQueryManager ---
	logger := zerolog.New(zerolog.NewConsoleWriter())
	servicemanager.RegisterSchema("TestSchema", types.GardenMonitorReadings{})
	environment := servicemanager.Environment{ProjectID: projectID, Location: "US"}

	// Create a real BigQuery client adapter for the manager
	bqAdapter, err := servicemanager.CreateGoogleBigQueryClient(ctx, projectID, bqConnection.ClientOptions...)
	require.NoError(t, err)

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
	// Verify Dataset
	_, err = bqClient.Dataset(datasetName).Metadata(ctx)
	assert.NoError(t, err, "BigQuery dataset should exist after creation")

	// Verify Table
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
	// Verify Dataset is gone
	_, err = bqClient.Dataset(datasetName).Metadata(ctx)
	assert.Error(t, err, "BigQuery dataset should NOT exist after teardown")
	t.Log("--- Deletion verification successful ---")
}
