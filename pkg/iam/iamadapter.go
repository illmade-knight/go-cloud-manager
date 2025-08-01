package iam

import "context"

// IAMClient defines the interface for low-level IAM operations.
// The concrete implementation is expected to be pre-configured with a project ID.
type IAMClient interface {
	EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error)

	AddResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error
	RemoveResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error

	AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error
	DeleteServiceAccount(ctx context.Context, accountName string) error
	AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error

	Close() error
}
