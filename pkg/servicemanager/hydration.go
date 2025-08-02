package servicemanager

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"strings"
)

// HydrateArchitecture iterates through a MicroserviceArchitecture and fills in
// default and dynamically generated values for all deployment specifications.
// It performs validation first, then proceeds in two main passes:
//  1. Hydrates basic names, regions, and container image paths for all services.
//  2. Injects environment variables into services based on the now-finalized
//     resource names they are explicitly linked to in the configuration.
func HydrateArchitecture(arch *MicroserviceArchitecture, defaultImageRepo, runID string, logger zerolog.Logger) error {
	if arch == nil {
		return errors.New("cannot hydrate a nil architecture")
	}

	// First, ensure the architecture definition is valid.
	validationErr := arch.Validate()
	if validationErr != nil {
		return fmt.Errorf("architecture validation failed: %w", validationErr)
	}

	defaultRegion := arch.Environment.Region
	log := logger.With().Str("component", "Hydration").Logger()

	// --- Pass 1: Hydrate basic deployment specs and names for all services ---
	log.Info().Msg("Starting hydration pass 1: Standard service properties and names.")

	// Hydrate the service manager first.
	if arch.ServiceManagerSpec.Deployment != nil {
		originalName := arch.ServiceManagerSpec.Name
		if runID != "" {
			arch.ServiceManagerSpec.Name = fmt.Sprintf("%s-%s", arch.ServiceManagerSpec.Name, runID)
			log.Info().Str("original_name", originalName).Str("new_name", arch.ServiceManagerSpec.Name).Str("run_id", runID).Msg("Service Manager name hydrated with run ID.")
		}
		hydrateDeploymentSpec(
			arch.ServiceManagerSpec.Deployment,
			arch.ServiceManagerSpec.Name,
			"conductor", // The ServiceManager belongs to the "conductor" pseudo-dataflow.
			arch.ProjectID,
			defaultRegion,
			defaultImageRepo,
			log,
		)
	}

	// Hydrate all services within dataflows.
	for dataflowName, dataflow := range arch.Dataflows {
		for serviceKey, service := range dataflow.Services {
			if service.Deployment == nil {
				continue
			}

			originalName := service.Name
			if runID != "" {
				// Create a new name with the runID and update the service spec.
				hydratedName := fmt.Sprintf("%s-%s", service.Name, runID)
				service.Name = hydratedName
				// REFACTOR_NOTE: It's crucial to re-assign the modified struct back to the map
				// because maps in Go hold copies of structs.
				dataflow.Services[serviceKey] = service
				log.Info().Str("original_name", originalName).Str("new_name", service.Name).Str("run_id", runID).Msg("Service name hydrated with run ID.")
			}

			hydrateDeploymentSpec(
				service.Deployment,
				service.Name,
				dataflowName,
				arch.ProjectID,
				defaultRegion,
				defaultImageRepo,
				log,
			)
		}
	}

	// --- Pass 2: Inject environment variables based on explicit resource links. ---
	// This makes the YAML purely declarative about which service uses which resource.
	log.Info().Msg("Starting hydration pass 2: Injecting environment variables from resource links.")
	for _, dataflow := range arch.Dataflows {
		// Inject environment variables for topics.
		for _, topic := range dataflow.Resources.Topics {
			if topic.ProducerService == "" {
				continue
			}
			if service, ok := dataflow.Services[topic.ProducerService]; ok {
				if service.Deployment.EnvironmentVars == nil {
					service.Deployment.EnvironmentVars = make(map[string]string)
				}
				envKey := fmt.Sprintf("%s_TOPIC_ID", strings.Replace(strings.ToUpper(topic.Name), "-", "_", -1))
				service.Deployment.EnvironmentVars[envKey] = topic.Name
				dataflow.Services[topic.ProducerService] = service
				log.Debug().Str("service", service.Name).Str("env_var", envKey).Str("value", topic.Name).Msg("Injected topic environment variable.")
			}
		}

		// Inject environment variables for subscriptions.
		for _, sub := range dataflow.Resources.Subscriptions {
			if sub.ConsumerService == "" {
				continue
			}
			if service, ok := dataflow.Services[sub.ConsumerService]; ok {
				if service.Deployment.EnvironmentVars == nil {
					service.Deployment.EnvironmentVars = make(map[string]string)
				}
				envKey := fmt.Sprintf("%s_SUB_ID", strings.Replace(strings.ToUpper(sub.Name), "-", "_", -1))
				service.Deployment.EnvironmentVars[envKey] = sub.Name
				dataflow.Services[sub.ConsumerService] = service
				log.Debug().Str("service", service.Name).Str("env_var", envKey).Str("value", sub.Name).Msg("Injected subscription environment variable.")
				service.Deployment.EnvironmentVars[envKey] = sub.Name
				dataflow.Services[sub.ConsumerService] = service
				log.Debug().Str("service", service.Name).Str("env_var", envKey).Str("value", sub.Name).Msg("Injected subscription environment variable.")
			}
		}
	}
	log.Info().Msg("Architecture hydration complete.")
	return nil
}

// hydrateDeploymentSpec is a private helper that fills in default and dynamically
// generated values for a single DeploymentSpec.
func hydrateDeploymentSpec(spec *DeploymentSpec, serviceName, dataflowName, projectID, defaultRegion, defaultImageRepo string, log zerolog.Logger) {
	log = log.With().Str("service", serviceName).Logger()

	if spec.Region == "" {
		spec.Region = defaultRegion
		log.Debug().Str("default_region", defaultRegion).Msg("Hydrated region with default.")
	}
	if spec.ImageRepo == "" {
		spec.ImageRepo = defaultImageRepo
		log.Debug().Str("default_image_repo", defaultImageRepo).Msg("Hydrated image repository with default.")
	}
	if spec.CPU == "" {
		spec.CPU = "1"
		log.Debug().Msg("Hydrated CPU with default '1'.")
	}
	if spec.Memory == "" {
		spec.Memory = "512Mi"
		log.Debug().Msg("Hydrated memory with default '512Mi'.")
	}

	if spec.EnvironmentVars == nil {
		spec.EnvironmentVars = make(map[string]string)
	}

	// Standard injected variables that are useful for service runtime introspection.
	spec.EnvironmentVars["PROJECT_ID"] = projectID
	spec.EnvironmentVars["SERVICE_NAME"] = serviceName
	spec.EnvironmentVars["DATAFLOW_NAME"] = dataflowName
	log.Debug().Msg("Injected standard PROJECT_ID, SERVICE_NAME, and DATAFLOW_NAME environment variables.")

	// Dynamically generate the full container image path with a unique tag.
	spec.Image = fmt.Sprintf(
		"%s-docker.pkg.dev/%s/%s/%s:%s",
		spec.Region,
		projectID,
		spec.ImageRepo,
		serviceName,
		uuid.New().String()[:8], // Append a unique ID to the tag to avoid collisions.
	)
	log.Debug().Str("image_path", spec.Image).Msg("Hydrated full container image path.")
}

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
