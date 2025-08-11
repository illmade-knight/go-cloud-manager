package servicemanager

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	// NEW_CODE: Using explicit import aliases to avoid name collisions
	// and make the distinction between the data client and admin client clear.
	firestoreadmin "cloud.google.com/go/firestore/apiv1/admin"
	firestoreadminpb "cloud.google.com/go/firestore/apiv1/admin/adminpb"
)

// --- Corrected Firestore Client and Handle Adapters ---

// gcpDatabaseHandleAdapter implements the DatabaseHandle for a Google Cloud Firestore database.
type gcpDatabaseHandleAdapter struct {
	// NEW_CODE: This now uses the correct AdminClient from the firestoreadmin package.
	adminClient *firestoreadmin.FirestoreAdminClient
	projectID   string
	databaseID  string
}

// Exists checks if the Firestore database exists.
func (a *gcpDatabaseHandleAdapter) Exists(ctx context.Context) (bool, error) {
	// NEW_CODE: This now correctly uses the admin client to get database details.
	req := &firestoreadminpb.GetDatabaseRequest{
		Name: fmt.Sprintf("projects/%s/databases/%s", a.projectID, a.databaseID),
	}
	_, err := a.adminClient.GetDatabase(ctx, req)

	if err == nil {
		return true, nil // Database exists.
	}
	if status.Code(err) == codes.NotFound {
		return false, nil // Database does not exist.
	}
	return false, fmt.Errorf("failed to check existence of firestore database '%s': %w", a.databaseID, err)
}

// Create creates the Firestore database. This is a long-running operation.
func (a *gcpDatabaseHandleAdapter) Create(ctx context.Context, locationID string, dbType string) error {
	// NEW_CODE: Using the correct protobuf types and client method.
	req := &firestoreadminpb.CreateDatabaseRequest{
		Parent:     fmt.Sprintf("projects/%s", a.projectID),
		DatabaseId: a.databaseID,
		Database: &firestoreadminpb.Database{
			LocationId: locationID,
			Type:       firestoreadminpb.Database_DatabaseType(firestoreadminpb.Database_DatabaseType_value[dbType]),
		},
	}
	op, err := a.adminClient.CreateDatabase(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to initiate firestore database creation: %w", err)
	}

	// Wait for the long-running operation to complete.
	_, err = op.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed while waiting for firestore database creation: %w", err)
	}
	return nil
}

// Delete is a placeholder as deleting Firestore databases is not a common or simple operation
// and is often disabled. For this implementation, we will return an error to prevent accidental use.
func (a *gcpDatabaseHandleAdapter) Delete(ctx context.Context) error {
	// Deleting a Firestore database is a destructive and protected operation.
	// It's safer to require manual deletion via the cloud console or gcloud CLI.
	return errors.New("programmatic deletion of firestore databases is not supported by the servicemanager")
}

// gcpFirestoreClientAdapter implements the DocumentStoreClient for Google Cloud Firestore.
type gcpFirestoreClientAdapter struct {
	// NEW_CODE: The adapter now holds the AdminClient for management operations.
	adminClient *firestoreadmin.FirestoreAdminClient
	projectID   string
}

// Database returns a handle to a specific Firestore database.
func (a *gcpFirestoreClientAdapter) Database(dbID string) DatabaseHandle {
	return &gcpDatabaseHandleAdapter{
		adminClient: a.adminClient,
		projectID:   a.projectID,
		databaseID:  dbID,
	}
}

// Close cleans up the admin client connection.
func (a *gcpFirestoreClientAdapter) Close() error {
	if a.adminClient != nil {
		return a.adminClient.Close()
	}
	return nil
}

// CreateGoogleFirestoreClient creates a new client for interacting with the Firestore Admin API.
// Note: This client is for administrative tasks (Create/Delete Database) and is distinct
// from the standard firestore.NewClient which is used for data operations (Get/Set documents).
func CreateGoogleFirestoreClient(ctx context.Context, projectID string, opts ...option.ClientOption) (DocumentStoreClient, error) {
	// NEW_CODE: Using the correct constructor from the aliased package.
	adminClient, err := firestoreadmin.NewFirestoreAdminClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create firestore admin client: %w", err)
	}

	return &gcpFirestoreClientAdapter{
		adminClient: adminClient,
		projectID:   projectID,
	}, nil
}
