package deployment

import "context"

// IAMClient defines the interface for low-level IAM operations.
// The concrete implementation is expected to be pre-configured with a project ID.
type IAMClient interface {
	EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error)
	AddResourceIAMBinding(ctx context.Context, resourceType, resourceID, role, member string) error
	RemoveResourceIAMBinding(ctx context.Context, resourceType, resourceID, role, member string) error
}
