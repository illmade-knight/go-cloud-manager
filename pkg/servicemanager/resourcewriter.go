package servicemanager

import (
	"context"
)

// ProvisionedResourceWriter defines the interface for writing the results of a provisioning operation.
type ProvisionedResourceWriter interface {
	// Write records the details of the provisioned resources.
	Write(ctx context.Context, resources *ProvisionedResources) error
	// Close any underlying connections or file handles.
	Close() error
}
