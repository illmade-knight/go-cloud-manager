package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
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

// HydrateArchitecture iterates through a MicroserviceArchitecture and fills in
// default and dynamically generated values for all deployment specifications.
// If a runID is provided, it will be appended to all service names for uniqueness.
func HydrateArchitecture(arch *MicroserviceArchitecture, defaultImageRepo, runID string) error {
	if arch == nil {
		return errors.New("cannot hydrate a nil architecture")
	}

	// Use top-level environment as the source for defaults.
	defaultRegion := arch.Environment.Region

	// First, hydrate the top-level ServiceManagerSpec's deployment.
	if arch.ServiceManagerSpec.Deployment != nil {
		// If a runID is provided, make the service name unique.
		if runID != "" {
			arch.ServiceManagerSpec.Name = fmt.Sprintf("%s-%s", arch.ServiceManagerSpec.Name, runID)
		}
		hydrateDeploymentSpec(
			arch.ServiceManagerSpec.Deployment,
			arch.ServiceManagerSpec.Name, // Use the (potentially updated) name
			arch.ProjectID,
			defaultRegion,
			defaultImageRepo,
		)
	}

	// Loop through all dataflows and their services.
	for _, dataflow := range arch.Dataflows {
		for serviceName, service := range dataflow.Services {
			// If a runID is provided, make the service name unique.
			if runID != "" {
				service.Name = fmt.Sprintf("%s-%s", service.Name, runID)
				// Update the map with the modified service spec
				dataflow.Services[serviceName] = service
			}

			if service.Deployment != nil {
				hydrateDeploymentSpec(
					service.Deployment,
					service.Name, // Use the (potentially updated) name
					arch.ProjectID,
					defaultRegion,
					defaultImageRepo,
				)
			}
		}
	}
	return nil
}

// hydrateDeploymentSpec is a private helper that fills in default and dynamically
// generated values for a single DeploymentSpec.
func hydrateDeploymentSpec(spec *DeploymentSpec, serviceName, projectID, defaultRegion, defaultImageRepo string) {
	// If Region is not specified in the spec, use the top-level default.
	if spec.Region == "" {
		spec.Region = defaultRegion
	}

	// If ImageRepo is not specified, use the default.
	if spec.ImageRepo == "" {
		spec.ImageRepo = defaultImageRepo
	}

	// Always generate a full, unique image tag, overwriting any existing one.
	spec.Image = fmt.Sprintf(
		"%s-docker.pkg.dev/%s/%s/%s:%s",
		spec.Region,
		projectID,
		spec.ImageRepo,
		serviceName,
		uuid.New().String()[:8],
	)
}
