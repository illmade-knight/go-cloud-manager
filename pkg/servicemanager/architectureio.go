package servicemanager

import (
	"context"
)

type ArchitectureLoader interface {
	// LoadArchitecture loads the complete microservice architecture from a given source (e.g., a file path).
	LoadArchitecture(ctx context.Context) (*MicroserviceArchitecture, error)
}

// ArchitectureIO defines a generic contract for reading service architecture
// configurations and writing provisioned resource state.
type ArchitectureIO interface {
	// ArchitectureLoader loads the complete microservice architecture from a given source (e.g., a file path).
	ArchitectureLoader
	// LoadResourceGroup loads a single resource group from a given source.
	LoadResourceGroup(ctx context.Context, groupName string) (*ResourceGroup, error)

	// WriteProvisionedResources writes the state of provisioned resources to a given destination (e.g., a file path).
	WriteProvisionedResources(ctx context.Context, resources *ProvisionedResources) error
}
