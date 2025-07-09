//go:build integration

package servicemanager_test

import (
	"cloud.google.com/go/storage"
	"context"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-iot/helpers/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

// TestStorageManager_Integration tests the manager against a live GCS emulator.
func TestStorageManager_Integration(t *testing.T) {
	ctx := context.Background()
	projectID := "gcs-it-project"
	runID := uuid.New().String()[:8]

	// --- 1. Define Resource Names and Configuration ---
	// Use the helper to generate a valid and unique bucket name.
	bucketName := servicemanager.GenerateTestBucketName("it-bucket-" + runID)

	// Initial configuration for the bucket
	initialResources := servicemanager.CloudResourcesSpec{
		GCSBuckets: []servicemanager.GCSBucket{
			{
				CloudResource: servicemanager.CloudResource{
					Name:   bucketName,
					Labels: map[string]string{"env": "test", "phase": "initial"},
				},
				Location:          "US",
				StorageClass:      "STANDARD",
				VersioningEnabled: false,
			},
		},
	}

	// --- 2. Setup Emulator and Real GCS Client ---
	t.Log("Setting up GCS emulator...")
	gcsConfig := emulators.GetDefaultGCSConfig(projectID, "")
	gcsConnection := emulators.SetupGCSEmulator(t, ctx, gcsConfig)
	gcsClient := emulators.GetStorageClient(t, ctx, gcsConfig, gcsConnection.ClientOptions)
	defer gcsClient.Close()

	// --- 3. Create the StorageManager ---
	logger := zerolog.New(zerolog.NewConsoleWriter())
	environment := servicemanager.Environment{ProjectID: projectID}
	storageAdapter := servicemanager.NewGCSClientAdapter(gcsClient)
	manager, err := servicemanager.NewStorageManager(storageAdapter, logger, environment)
	require.NoError(t, err)

	// =========================================================================
	// --- Phase 1: CREATE Resource ---
	// =========================================================================
	t.Log("--- Starting CreateResources (Initial) ---")
	provisioned, err := manager.CreateResources(ctx, initialResources)
	require.NoError(t, err)
	assert.Len(t, provisioned, 1, "Should provision one bucket")
	t.Log("--- CreateResources finished successfully ---")

	// =========================================================================
	// --- Phase 2: VERIFY Resource Exists with Correct Attributes ---
	// =========================================================================
	t.Log("--- Verifying resource existence and initial attributes ---")
	attrs, err := gcsClient.Bucket(bucketName).Attrs(ctx)
	require.NoError(t, err, "GCS bucket should exist after creation")
	assert.Equal(t, "STANDARD", attrs.StorageClass, "StorageClass should be STANDARD")
	assert.Equal(t, "initial", attrs.Labels["phase"], "Label 'phase' should be 'initial'")
	t.Log("--- Verification successful ---")

	// =========================================================================
	// --- Phase 3: UPDATE Resource ---
	// =========================================================================
	t.Log("--- Starting CreateResources (Update) ---")
	// Define an updated configuration for the same bucket
	updatedResources := servicemanager.CloudResourcesSpec{
		GCSBuckets: []servicemanager.GCSBucket{
			{
				CloudResource: servicemanager.CloudResource{
					Name:   bucketName,
					Labels: map[string]string{"env": "test", "phase": "updated"}, // Change a label
				},
				StorageClass:      "NEARLINE", // Change storage class
				VersioningEnabled: true,       // Enable versioning
			},
		},
	}
	// Calling CreateResources again on an existing resource should trigger an update.
	_, err = manager.CreateResources(ctx, updatedResources)
	require.NoError(t, err)
	t.Log("--- Update finished successfully ---")

	// =========================================================================
	// --- Phase 4: VERIFY Updated Attributes ---
	// =========================================================================
	t.Log("--- Verifying updated attributes ---")
	updatedAttrs, err := gcsClient.Bucket(bucketName).Attrs(ctx)
	require.NoError(t, err)
	assert.Equal(t, "NEARLINE", updatedAttrs.StorageClass, "StorageClass should be updated to NEARLINE")
	assert.True(t, updatedAttrs.VersioningEnabled, "Versioning should be enabled after update")
	assert.Equal(t, "updated", updatedAttrs.Labels["phase"], "Label 'phase' should be 'updated'")
	t.Log("--- Verification of update successful ---")

	// =========================================================================
	// --- Phase 5: TEARDOWN Resource ---
	// =========================================================================
	t.Log("--- Starting Teardown ---")
	err = manager.Teardown(ctx, initialResources) // Use any spec that contains the bucket name
	require.NoError(t, err, "Teardown should not fail")
	t.Log("--- Teardown finished successfully ---")

	// =========================================================================
	// --- Phase 6: VERIFY Resource is Deleted ---
	// =========================================================================
	t.Log("--- Verifying resource is deleted from emulator ---")
	_, err = gcsClient.Bucket(bucketName).Attrs(ctx)
	assert.ErrorIs(t, err, storage.ErrBucketNotExist, "GCS bucket should NOT exist after teardown")
	t.Log("--- Deletion verification successful ---")
}
