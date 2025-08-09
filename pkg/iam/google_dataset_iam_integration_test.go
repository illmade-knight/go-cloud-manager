//go:build integration

package iam_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-test/auth"
	"github.com/stretchr/testify/require"
)

// TestBigQueryIAMManager_Lifecycle verifies the full add, check, and remove lifecycle for BigQuery IAM bindings.
func TestBigQueryIAMManager_Lifecycle(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create the standard BigQuery client, which will be passed to the manager.
	bqClient, err := bigquery.NewClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bqClient.Close() })

	// Create the dedicated BigQueryIAMManager instance.
	bqIAMManager, err := iam.NewBigQueryIAMManager(bqClient)
	require.NoError(t, err)

	// Create a temporary BigQuery dataset for the test.
	datasetID := fmt.Sprintf("test_bq_iam_manager_%d", time.Now().UnixNano())
	dataset := bqClient.Dataset(datasetID)
	err = dataset.Create(ctx, &bigquery.DatasetMetadata{Location: "US"})
	require.NoError(t, err)
	t.Logf("Successfully created test BigQuery dataset: %s", datasetID)
	t.Cleanup(func() {
		t.Logf("Cleaning up test BigQuery dataset: %s", datasetID)
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer deleteCancel()
		require.NoError(t, dataset.DeleteWithContents(deleteCtx))
	})

	// Define the member and role to test.
	member := fmt.Sprintf("serviceAccount:%s-compute@developer.gserviceaccount.com", projectNumber)
	role := "roles/bigquery.dataViewer"

	// --- Act & Assert ---

	// 1. Initial State: Verify the binding does not exist.
	t.Log("Verifying initial state: binding should not exist...")
	found, err := bqIAMManager.CheckDatasetIAMBinding(ctx, datasetID, role, member)
	require.NoError(t, err)
	require.False(t, found, "Binding should not exist initially for dataset")
	t.Log("✅ Initial state verified.")

	// 2. Add Binding: Grant the role to the member.
	t.Logf("Adding role '%s' to member '%s'...", role, member)
	err = bqIAMManager.AddDatasetIAMBinding(ctx, datasetID, role, member)
	require.NoError(t, err)
	t.Log("AddDatasetIAMBinding completed.")

	// 3. Verify Binding Exists: Use Eventually to handle IAM propagation delay.
	t.Log("Verifying binding existence (polling)...")
	// REFACTOR: Added detailed diagnostic logging to the polling function.
	require.Eventually(t, func() bool {
		found, err := bqIAMManager.CheckDatasetIAMBinding(ctx, datasetID, role, member)
		if err != nil {
			t.Logf("Polling for binding existence failed with error: %v", err)
			return false // Don't continue if the check itself errors.
		}
		if !found {
			// If the binding isn't found, fetch and print the current ACL for debugging.
			meta, metaErr := bqClient.Dataset(datasetID).Metadata(ctx)
			if metaErr != nil {
				t.Logf("Failed to get metadata for diagnostics: %v", metaErr)
			} else {
				t.Logf("Binding not found. Current ACL for dataset %s:", datasetID)
				for i, entry := range meta.Access {
					t.Logf("  Entry %d: Role='%s', EntityType='%v', Entity='%s'", i, entry.Role, entry.EntityType, entry.Entity)
				}
			}
		}
		return found
	}, 2*time.Minute, 5*time.Second, "Binding for dataset did not propagate in time")
	t.Log("✅ Binding existence verified.")

	// 4. Remove Binding: Revoke the role from the member.
	t.Logf("Removing role '%s' from member '%s'...", role, member)
	err = bqIAMManager.RemoveDatasetIAMBinding(ctx, datasetID, role, member)
	require.NoError(t, err)
	t.Log("RemoveDatasetIAMBinding completed.")

	// 5. Verify Binding is Removed: Use Eventually to handle propagation delay.
	t.Log("Verifying binding removal (polling)...")
	// REFACTOR: Added detailed diagnostic logging to the polling function.
	require.Eventually(t, func() bool {
		found, err := bqIAMManager.CheckDatasetIAMBinding(ctx, datasetID, role, member)
		if err != nil {
			t.Logf("Polling for binding removal failed with error: %v", err)
			return false
		}
		if found {
			// If the binding is unexpectedly found, print the current ACL for debugging.
			meta, metaErr := bqClient.Dataset(datasetID).Metadata(ctx)
			if metaErr != nil {
				t.Logf("Failed to get metadata for diagnostics: %v", metaErr)
			} else {
				t.Logf("Binding unexpectedly found. Current ACL for dataset %s:", datasetID)
				for i, entry := range meta.Access {
					t.Logf("  Entry %d: Role='%s', EntityType='%v', Entity='%s'", i, entry.Role, entry.EntityType, entry.Entity)
				}
			}
		}
		return !found
	}, 2*time.Minute, 5*time.Second, "Binding for dataset was not removed in time")
	t.Log("✅ Binding removal verified.")
}
