//go:build integration

package servicemanager_test

import (
	"cloud.google.com/go/storage"
	"context"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// TestStorageManager_Integration tests the manager against a live GCS emulator,
// covering the full Create, Update, and Teardown lifecycle.
func TestStorageManager_Integration(t *testing.T) {
	// --- ARRANGE: Set up context, configuration, and clients ---
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	projectID := "gcs-it-project"
	runID := uuid.New().String()[:8]
	bucketName := servicemanager.GenerateTestBucketName("it-bucket-" + runID)

	// Set up the GCS emulator and a client connected to it.
	t.Log("Setting up GCS emulator...")
	gcsConfig := emulators.GetDefaultGCSConfig(projectID, "")
	gcsConnection := emulators.SetupGCSEmulator(t, ctx, gcsConfig)
	gcsClient := emulators.GetStorageClient(t, ctx, gcsConfig, gcsConnection.ClientOptions)
	t.Cleanup(func() {
		err := gcsClient.Close()
		if err != nil {
			t.Logf("Error closing GCS client: %v", err)
		}
	})

	// Create the StorageManager instance.
	logger := zerolog.New(zerolog.NewConsoleWriter())
	environment := servicemanager.Environment{ProjectID: projectID}
	storageAdapter := servicemanager.NewGCSClientAdapter(gcsClient)
	manager, err := servicemanager.NewStorageManager(storageAdapter, logger, environment)
	require.NoError(t, err)

	// --- ACT & ASSERT: Execute and verify each lifecycle phase ---

	// Phase 1: CREATE Resource
	t.Run("CreateResource", func(t *testing.T) {
		t.Log("--- Starting CreateResources (Initial) ---")
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

		provisioned, createErr := manager.CreateResources(ctx, initialResources)
		require.NoError(t, createErr)
		assert.Len(t, provisioned, 1, "Should provision one bucket")
		t.Log("--- CreateResources finished successfully ---")

		// Verify initial attributes immediately after creation.
		attrs, attrsErr := gcsClient.Bucket(bucketName).Attrs(ctx)
		require.NoError(t, attrsErr, "GCS bucket should exist after creation")
		assert.Equal(t, "STANDARD", attrs.StorageClass)
		assert.Equal(t, "initial", attrs.Labels["phase"])
	})

	// Phase 2: UPDATE Resource
	t.Run("UpdateResource", func(t *testing.T) {
		t.Log("--- Starting CreateResources (Update) ---")
		updatedResources := servicemanager.CloudResourcesSpec{
			GCSBuckets: []servicemanager.GCSBucket{
				{
					CloudResource: servicemanager.CloudResource{
						Name:   bucketName,
						Labels: map[string]string{"env": "test", "phase": "updated"},
					},
					StorageClass:      "NEARLINE",
					VersioningEnabled: true,
				},
			},
		}
		// Calling CreateResources again on an existing resource triggers an update.
		_, updateErr := manager.CreateResources(ctx, updatedResources)
		require.NoError(t, updateErr)
		t.Log("--- Update finished successfully ---")

		// Verify updated attributes.
		updatedAttrs, attrsErr := gcsClient.Bucket(bucketName).Attrs(ctx)
		require.NoError(t, attrsErr)
		assert.Equal(t, "NEARLINE", updatedAttrs.StorageClass)
		assert.True(t, updatedAttrs.VersioningEnabled)
		assert.Equal(t, "updated", updatedAttrs.Labels["phase"])
	})

	// Phase 3: TEARDOWN Resource
	t.Run("TeardownResource", func(t *testing.T) {
		t.Log("--- Starting Teardown ---")
		// Teardown spec only needs the resource name.
		resourcesForTeardown := servicemanager.CloudResourcesSpec{
			GCSBuckets: []servicemanager.GCSBucket{{CloudResource: servicemanager.CloudResource{Name: bucketName}}},
		}
		teardownErr := manager.Teardown(ctx, resourcesForTeardown)
		require.NoError(t, teardownErr)
		t.Log("--- Teardown finished successfully ---")

		// Verify resource is deleted.
		_, attrsErr := gcsClient.Bucket(bucketName).Attrs(ctx)
		assert.ErrorIs(t, attrsErr, storage.ErrBucketNotExist, "GCS bucket should NOT exist after teardown")
	})
}
