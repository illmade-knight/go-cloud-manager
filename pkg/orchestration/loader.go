package orchestration

import (
	"context"
	"errors"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
)

// EmbeddedArchitectureLoader is a simple implementation of the servicemanager.ArchitectureIO
// interface that works with a pre-existing, in-memory MicroserviceArchitecture struct.
// This is primarily useful for scenarios where the architecture is defined statically,
// for example, from a `go:embed` directive.
type EmbeddedArchitectureLoader struct {
	architecture *servicemanager.MicroserviceArchitecture
}

// NewEmbeddedArchitectureLoader creates a new loader that serves a pre-parsed architecture.
func NewEmbeddedArchitectureLoader(arch *servicemanager.MicroserviceArchitecture) *EmbeddedArchitectureLoader {
	return &EmbeddedArchitectureLoader{
		architecture: arch,
	}
}

// LoadArchitecture simply returns the pre-parsed architecture from the embedded data.
func (l *EmbeddedArchitectureLoader) LoadArchitecture(_ context.Context) (*servicemanager.MicroserviceArchitecture, error) {
	if l.architecture == nil {
		return nil, errors.New("embedded architecture was not loaded or is nil")
	}
	return l.architecture, nil
}

// LoadResourceGroup is not implemented for the embedded loader.
func (l *EmbeddedArchitectureLoader) LoadResourceGroup(_ context.Context, _ string) (*servicemanager.ResourceGroup, error) {
	return nil, errors.New("LoadResourceGroup is not implemented for the embedded loader")
}

// WriteProvisionedResources is not implemented for the embedded loader.
func (l *EmbeddedArchitectureLoader) WriteProvisionedResources(_ context.Context, _ *servicemanager.ProvisionedResources) error {
	return errors.New("WriteProvisionedResources is not implemented for the embedded loader")
}
