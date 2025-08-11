package servicemanager

import (
	"context"
)

// DatabaseHandle defines an interface for interacting with a specific document database.
// It provides methods for checking existence, creating, and deleting the database instance.
type DatabaseHandle interface {
	// Exists checks if the database exists in the project.
	Exists(ctx context.Context) (bool, error)
	// Create provisions a new database with the specified location and type.
	Create(ctx context.Context, locationID string, dbType string) error
	// Delete removes the database. This is a destructive operation.
	Delete(ctx context.Context) error
}

// DocumentStoreClient defines a generic interface for a client that can interact
// with a NoSQL document store service (like Google Cloud Firestore).
type DocumentStoreClient interface {
	// Database returns a handle for a specific database within the project.
	// For Google Cloud, the database ID is typically "(default)".
	Database(dbID string) DatabaseHandle
	// Close terminates the client's connection to the service.
	Close() error
}
