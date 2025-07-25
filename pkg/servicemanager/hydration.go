package servicemanager

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
)

// Validate performs a pre-flight check on the architecture definition to ensure
// all required fields are populated before attempting to hydrate or deploy.
func (arch *MicroserviceArchitecture) Validate() error {
	if arch.ServiceManagerSpec.Name == "" {
		return errors.New("ServiceManagerSpec.Name is a required field")
	}
	if arch.ServiceManagerSpec.ServiceAccount == "" {
		return errors.New("ServiceManagerSpec.ServiceAccount is a required field")
	}

	for dfName, dataflow := range arch.Dataflows {
		for serviceKey, service := range dataflow.Services {
			if service.Name == "" {
				return fmt.Errorf("service with key '%s' in dataflow '%s' is missing the required 'Name' field", serviceKey, dfName)
			}
			if service.ServiceAccount == "" {
				return fmt.Errorf("service '%s' in dataflow '%s' is missing the required 'ServiceAccount' field", service.Name, dfName)
			}
			if service.Deployment == nil {
				return fmt.Errorf("service '%s' in dataflow '%s' is missing the required 'Deployment' spec", service.Name, dfName)
			}
			if service.Deployment.SourcePath == "" {
				return fmt.Errorf("deployment spec for service '%s' is missing the required 'SourcePath'", service.Name)
			}
		}
	}
	return nil
}

// HydrateArchitecture iterates through a MicroserviceArchitecture and fills in
// default and dynamically generated values for all deployment specifications.
func HydrateArchitecture(arch *MicroserviceArchitecture, defaultImageRepo, runID string) error {
	if arch == nil {
		return errors.New("cannot hydrate a nil architecture")
	}

	// --- THIS IS THE FIX ---
	// Perform a pre-flight validation check before doing any work.
	if err := arch.Validate(); err != nil {
		return fmt.Errorf("architecture validation failed: %w", err)
	}
	// --- END OF FIX ---

	defaultRegion := arch.Environment.Region

	if arch.ServiceManagerSpec.Deployment != nil {
		if runID != "" {
			arch.ServiceManagerSpec.Name = fmt.Sprintf("%s-%s", arch.ServiceManagerSpec.Name, runID)
		}
		hydrateDeploymentSpec(
			arch.ServiceManagerSpec.Deployment,
			arch.ServiceManagerSpec.Name,
			arch.ProjectID,
			defaultRegion,
			defaultImageRepo,
		)
	}

	for _, dataflow := range arch.Dataflows {
		for serviceKey, service := range dataflow.Services {
			// NOTE: We use the serviceKey to update the map, but the service.Name for hydration.
			// This is important because the name might be modified by the runID.
			if runID != "" {
				service.Name = fmt.Sprintf("%s-%s", service.Name, runID)
				dataflow.Services[serviceKey] = service // Re-assign the modified struct back to the map
			}

			if service.Deployment != nil {
				hydrateDeploymentSpec(
					service.Deployment,
					service.Name,
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
	if spec.Region == "" {
		spec.Region = defaultRegion
	}
	if spec.ImageRepo == "" {
		spec.ImageRepo = defaultImageRepo
	}
	if spec.CPU == "" {
		spec.CPU = "1"
	}
	if spec.Memory == "" {
		spec.Memory = "512Mi"
	}

	if spec.EnvironmentVars != nil {
		if _, ok := spec.EnvironmentVars["PROJECT_ID"]; ok {
			spec.EnvironmentVars["PROJECT_ID"] = projectID
		}
	}

	spec.Image = fmt.Sprintf(
		"%s-docker.pkg.dev/%s/%s/%s:%s",
		spec.Region,
		projectID,
		spec.ImageRepo,
		serviceName,
		uuid.New().String()[:8],
	)
}
