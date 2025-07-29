package servicemanager

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
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

	if err := arch.Validate(); err != nil {
		return fmt.Errorf("architecture validation failed: %w", err)
	}

	defaultRegion := arch.Environment.Region

	// Hydrate the service manager first
	if arch.ServiceManagerSpec.Deployment != nil {
		if runID != "" {
			arch.ServiceManagerSpec.Name = fmt.Sprintf("%s-%s", arch.ServiceManagerSpec.Name, runID)
		}
		hydrateDeploymentSpec(
			arch.ServiceManagerSpec.Deployment,
			arch.ServiceManagerSpec.Name,
			"conductor",
			arch.ProjectID,
			defaultRegion,
			defaultImageRepo,
		)
	}

	// First pass: Hydrate basic deployment specs and names for all services
	for dataflowName, dataflow := range arch.Dataflows {
		for serviceKey, service := range dataflow.Services {
			if runID != "" {
				service.Name = fmt.Sprintf("%s-%s", service.Name, runID)
				dataflow.Services[serviceKey] = service // Re-assign the modified struct back to the map
			}

			if service.Deployment != nil {
				hydrateDeploymentSpec(
					service.Deployment,
					service.Name,
					dataflowName,
					arch.ProjectID,
					defaultRegion,
					defaultImageRepo,
				)
			}
		}
	}

	// REFACTOR: Second pass to inject environment variables based on explicit resource links.
	// This makes the YAML purely declarative about which service uses which resource.
	for _, dataflow := range arch.Dataflows {
		// Inject environment variables for topics
		for _, topic := range dataflow.Resources.Topics {
			if topic.ProducerService != "" {
				if service, ok := dataflow.Services[topic.ProducerService]; ok {
					if service.Deployment.EnvironmentVars == nil {
						service.Deployment.EnvironmentVars = make(map[string]string)
					}
					envKey := fmt.Sprintf("%s_TOPIC_ID", strings.ToUpper(topic.Name))
					service.Deployment.EnvironmentVars[envKey] = topic.Name
					dataflow.Services[topic.ProducerService] = service
				}
			}
		}

		// Inject environment variables for subscriptions
		for _, sub := range dataflow.Resources.Subscriptions {
			if sub.ConsumerService != "" {
				if service, ok := dataflow.Services[sub.ConsumerService]; ok {
					if service.Deployment.EnvironmentVars == nil {
						service.Deployment.EnvironmentVars = make(map[string]string)
					}
					envKey := fmt.Sprintf("%s_SUBSCRIPTION_ID", strings.ToUpper(sub.Name))
					service.Deployment.EnvironmentVars[envKey] = sub.Name
					dataflow.Services[sub.ConsumerService] = service
				}
			}
		}
	}

	return nil
}

// hydrateDeploymentSpec is a private helper that fills in default and dynamically
// generated values for a single DeploymentSpec.
func hydrateDeploymentSpec(spec *DeploymentSpec, serviceName, dataflowName, projectID, defaultRegion, defaultImageRepo string) {
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

	if spec.EnvironmentVars == nil {
		spec.EnvironmentVars = make(map[string]string)
	}

	// Standard injected variables
	spec.EnvironmentVars["PROJECT_ID"] = projectID
	spec.EnvironmentVars["SERVICE_NAME"] = serviceName
	spec.EnvironmentVars["DATAFLOW_NAME"] = dataflowName

	spec.Image = fmt.Sprintf(
		"%s-docker.pkg.dev/%s/%s/%s:%s",
		spec.Region,
		projectID,
		spec.ImageRepo,
		serviceName,
		uuid.New().String()[:8],
	)
}
