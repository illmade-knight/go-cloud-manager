package orchestration

import (
	"context"
	"fmt"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
)

// EmbeddedArchitectureLoader is a simple implementation of the ArchitectureLoader
// interface that works with the embedded services.yaml file.
type EmbeddedArchitectureLoader struct {
	architecture *servicemanager.MicroserviceArchitecture
}

func NewEmbeddedArchitectureLoader(arch *servicemanager.MicroserviceArchitecture) *EmbeddedArchitectureLoader {
	return &EmbeddedArchitectureLoader{
		architecture: arch,
	}
}

func (l *EmbeddedArchitectureLoader) LoadResourceGroup(ctx context.Context, groupName string) (*servicemanager.ResourceGroup, error) {
	//TODO implement me
	panic("implement me")
}

func (l *EmbeddedArchitectureLoader) WriteProvisionedResources(ctx context.Context, resources *servicemanager.ProvisionedResources) error {
	//TODO implement me
	panic("implement me")
}

// LoadArchitecture simply returns the pre-parsed architecture from the embedded data.
func (l *EmbeddedArchitectureLoader) LoadArchitecture(ctx context.Context) (*servicemanager.MicroserviceArchitecture, error) {
	if l.architecture == nil {
		return nil, fmt.Errorf("embedded architecture was not loaded")
	}
	return l.architecture, nil
}
