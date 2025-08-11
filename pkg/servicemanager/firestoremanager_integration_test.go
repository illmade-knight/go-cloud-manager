//go:build integration

package servicemanager_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// REFACTOR_NOTE: The following two structs are test-only adapters that
// implement the DocumentStoreClient and DatabaseHandle interfaces. They are
// designed specifically to work around the Firestore emulator's limitations,
// allowing us to test the production FirestoreManager code without modification.

// firestoreEmulatorHandle implements the DatabaseHandle for the Firestore emulator.
type firestoreEmulatorHandle struct {
	projectID string
	opts      []option.ClientOption
}

// Exists for the emulator is inferred by successfully creating a data-plane client.
func (h *firestoreEmulatorHandle) Exists(ctx context.Context) (bool, error) {
	// The emulator doesn't support the Admin Exists API. We infer existence
	// by creating a data-plane client. If it connects, the DB "exists".
	client, err := firestore.NewClient(ctx, h.projectID, h.opts...)
	if err != nil {
		return false, fmt.Errorf("emulator existence check failed via data client: %w", err)
	}
	// Close immediately, we just needed to check the connection.
	_ = client.Close()
	return true, nil
}

// Create is a no-op because the emulator provides a database on startup.
func (h *firestoreEmulatorHandle) Create(ctx context.Context, locationID, dbType string) error {
	return nil
}

// Delete is a no-op as the test will clean up the whole container.
func (h *firestoreEmulatorHandle) Delete(ctx context.Context) error {
	return nil
}

// firestoreEmulatorClientAdapter implements DocumentStoreClient for the emulator.
type firestoreEmulatorClientAdapter struct {
	projectID string
	opts      []option.ClientOption
}

func (a *firestoreEmulatorClientAdapter) Database(dbID string) servicemanager.DatabaseHandle {
	return &firestoreEmulatorHandle{projectID: a.projectID, opts: a.opts}
}

func (a *firestoreEmulatorClientAdapter) Close() error { return nil }

// TestFirestoreManager_Integration tests the manager's lifecycle against a live Firestore emulator.
func TestFirestoreManager_Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	projectID := fmt.Sprintf("fs-it-%s", uuid.NewString()[:8])
	resources := servicemanager.CloudResourcesSpec{
		FirestoreDatabases: []servicemanager.FirestoreDatabase{
			{
				CloudResource: servicemanager.CloudResource{Name: "default-db"},
				LocationID:    "nam5",
				Type:          servicemanager.FirestoreModeNative,
			},
		},
	}

	t.Log("Setting up Firestore emulator...")
	fsConfig := emulators.GetDefaultFirestoreConfig(projectID)
	fsConnection := emulators.SetupFirestoreEmulator(t, ctx, fsConfig)

	// REFACTOR_NOTE: Instead of creating the real Google client, we instantiate
	// our test-only adapter. The manager under test doesn't know the difference.
	firestoreTestAdapter := &firestoreEmulatorClientAdapter{
		projectID: projectID,
		opts:      fsConnection.ClientOptions,
	}

	logger := zerolog.New(zerolog.NewConsoleWriter())
	environment := servicemanager.Environment{ProjectID: projectID}
	manager, err := servicemanager.NewFirestoreManager(firestoreTestAdapter, logger, environment)
	require.NoError(t, err)

	// --- ACT & ASSERT: Execute and verify each lifecycle phase ---

	// The test logic remains the same, but now it will succeed because the
	// underlying adapter handles the emulator's behavior correctly.
	t.Run("CreateResource", func(t *testing.T) {
		t.Log("--- Starting CreateResources ---")
		provisioned, createErr := manager.CreateResources(ctx, resources)
		require.NoError(t, createErr)
		assert.Len(t, provisioned, 1, "Should provision one database")
		t.Log("--- CreateResources finished successfully ---")
	})

	t.Run("VerifyResourceExists", func(t *testing.T) {
		t.Log("--- Verifying resource exists in emulator ---")
		verifyErr := manager.Verify(ctx, resources)
		assert.NoError(t, verifyErr, "Firestore database verification should pass against the emulator")
		t.Log("--- Verification successful ---")
	})

	t.Run("TeardownResource", func(t *testing.T) {
		t.Log("--- Starting Teardown ---")
		teardownErr := manager.Teardown(ctx, resources)
		require.NoError(t, teardownErr, "Teardown should not fail")
		t.Log("--- Teardown finished successfully (by skipping) ---")

		verifyErr := manager.Verify(ctx, resources)
		assert.NoError(t, verifyErr, "Firestore database should NOT be deleted after teardown")
		t.Log("--- Deletion skip verification successful ---")
	})
}
