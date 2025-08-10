package iam

import "context"

// IAMClient defines the interface for low-level IAM operations.
// The concrete implementation is expected to be pre-configured with a project ID.
type IAMClient interface {
	// Service Account Lifecycle
	EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error)
	DeleteServiceAccount(ctx context.Context, accountName string) error
	GetServiceAccount(ctx context.Context, accountEmail string) error
	AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error

	// Resource IAM Policies
	ApplyIAMPolicy(ctx context.Context, binding PolicyBinding) error
	AddResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error
	RemoveResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error
	CheckResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) (bool, error)

	// Specialized IAM
	AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error

	// General
	Close() error
}
