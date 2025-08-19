package servicemanager

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// HydrateArchitecture prepares an architecture for a production-like deployment. It fills
// in default values and injects environment variables based on the static resource
// links in the YAML, without applying any test-specific transformations.
func HydrateArchitecture(arch *MicroserviceArchitecture, defaultImageRepo string, logger zerolog.Logger) error {
	if arch == nil {
		return errors.New("cannot hydrate a nil architecture")
	}
	err := arch.Validate()
	if err != nil {
		return fmt.Errorf("architecture validation failed: %w", err)
	}

	log := logger.With().Str("component", "Hydration").Logger()
	log.Info().Msg("Starting production architecture hydration...")

	// Step 1: Hydrate deployment specs with defaults and generate final image paths.
	hydrateAllDeploymentSpecs(arch, defaultImageRepo)

	// Step 2: Inject environment variables using the final, static names.
	injectAllEnvironmentVariables(arch)

	log.Info().Msg("Production architecture hydration complete.")
	return nil
}

// HydrateTestArchitecture prepares an architecture for an isolated test run. It first
// transforms all names, keys, and cross-references using the provided runID, then
// hydrates the deployment specs and injects environment variables based on the new names.
func HydrateTestArchitecture(arch *MicroserviceArchitecture, defaultImageRepo, runID string, logger zerolog.Logger) (map[string]string, error) {
	if runID == "" {
		return nil, errors.New("HydrateTestArchitecture requires a non-empty runID")
	}
	if arch == nil {
		return nil, errors.New("cannot hydrate a nil architecture")
	}
	err := arch.Validate()
	if err != nil {
		return nil, fmt.Errorf("architecture validation failed: %w", err)
	}

	log := logger.With().Str("component", "Hydration").Logger()
	log.Info().Msgf("Starting test architecture hydration with runID '%s'...", runID)

	nameMap := make(map[string]string)

	// Step 1: Apply the runID transformation to all names, keys, and links.
	applyRunIDTransformation(arch, runID, nameMap)

	// Step 2: Hydrate deployment specs with defaults, now using the transformed names.
	hydrateAllDeploymentSpecs(arch, defaultImageRepo)

	// Step 3: Inject environment variables using the final, transformed names.
	injectAllEnvironmentVariables(arch)

	log.Info().Msg("Test architecture hydration complete.")
	return nameMap, nil
}

// --- Private Helpers ---

// applyRunIDTransformation modifies all names, keys, and cross-references in the architecture.
func applyRunIDTransformation(arch *MicroserviceArchitecture, runID string, nameMap map[string]string) {
	if arch.ServiceManagerSpec.Deployment != nil {
		originalName := arch.ServiceManagerSpec.Name
		hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
		arch.ServiceManagerSpec.Name = hydratedName
		arch.ServiceManagerSpec.ServiceAccount = fmt.Sprintf("%s-%s", arch.ServiceManagerSpec.ServiceAccount, runID)
		nameMap[originalName] = hydratedName

		for key, val := range arch.ServiceManagerSpec.Deployment.EnvironmentVars {
			hydratedVal := fmt.Sprintf("%s-%s", val, runID)
			arch.ServiceManagerSpec.Deployment.EnvironmentVars[key] = hydratedVal
			nameMap[val] = hydratedVal
		}
	}

	for dataflowName, dataflow := range arch.Dataflows {
		for k, service := range dataflow.Services {
			originalName := service.Name
			hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
			service.Name = hydratedName
			service.ServiceAccount = fmt.Sprintf("%s-%s", service.ServiceAccount, runID)
			dataflow.Services[k] = service
			nameMap[originalName] = hydratedName
		}
		hydrateResourceNamesWithRunID(&dataflow.Resources, runID, nameMap)

		rekeyedServices := make(map[string]ServiceSpec, len(dataflow.Services))
		for _, service := range dataflow.Services {
			rekeyedServices[service.Name] = service
		}
		dataflow.Services = rekeyedServices

		updateAllCrossReferences(&dataflow, runID)
		arch.Dataflows[dataflowName] = dataflow
	}
}

// hydrateAllDeploymentSpecs fills in defaults for all services in the architecture.
func hydrateAllDeploymentSpecs(arch *MicroserviceArchitecture, defaultImageRepo string) {
	if arch.ServiceManagerSpec.Deployment != nil {
		hydrateDeploymentSpec(arch.ServiceManagerSpec.Deployment, arch.ServiceManagerSpec.Name, "conductor", arch.ProjectID, arch.Region, defaultImageRepo)
	}
	for dataflowName, dataflow := range arch.Dataflows {
		for _, service := range dataflow.Services {
			if service.Deployment != nil {
				hydrateDeploymentSpec(service.Deployment, service.Name, dataflowName, arch.ProjectID, arch.Region, defaultImageRepo)
			}
		}
	}
}

// injectAllEnvironmentVariables handles the injection of all resource links as env vars.
func injectAllEnvironmentVariables(arch *MicroserviceArchitecture) {
	for _, dataflow := range arch.Dataflows {
		for _, topic := range dataflow.Resources.Topics {
			if topic.ProducerService != nil && topic.ProducerService.Lookup.Method == LookupEnv {
				if service, ok := dataflow.Services[topic.ProducerService.Name]; ok {
					injectEnvVar(service.Deployment, topic.ProducerService.Lookup, topic.Name, "TOPIC_ID")
				}
			}
		}
		for _, sub := range dataflow.Resources.Subscriptions {
			if sub.ConsumerService != nil && sub.ConsumerService.Lookup.Method == LookupEnv {
				if service, ok := dataflow.Services[sub.ConsumerService.Name]; ok {
					injectEnvVar(service.Deployment, sub.ConsumerService.Lookup, sub.Name, "SUB_ID")
				}
			}
		}
		for _, table := range dataflow.Resources.BigQueryTables {
			for _, producer := range table.Producers {
				if producer.Lookup.Method != LookupEnv {
					continue
				}
				if service, ok := dataflow.Services[producer.Name]; ok {
					injectEnvVar(service.Deployment, producer.Lookup, table.Name, "WRITE_TABLE")
				}
			}
			for _, consumer := range table.Consumers {
				if consumer.Lookup.Method != LookupEnv {
					continue
				}
				if service, ok := dataflow.Services[consumer.Name]; ok {
					injectEnvVar(service.Deployment, consumer.Lookup, table.Name, "READ_TABLE")
				}
			}
		}
		for _, bucket := range dataflow.Resources.GCSBuckets {
			for _, producer := range bucket.Producers {
				if producer.Lookup.Method != LookupEnv {
					continue
				}
				if service, ok := dataflow.Services[producer.Name]; ok {
					injectEnvVar(service.Deployment, producer.Lookup, bucket.Name, "WRITE_BUCKET")
				}
			}
			for _, consumer := range bucket.Consumers {
				if consumer.Lookup.Method != LookupEnv {
					continue
				}
				if service, ok := dataflow.Services[consumer.Name]; ok {
					injectEnvVar(service.Deployment, consumer.Lookup, bucket.Name, "READ_BUCKET")
				}
			}
		}
		// NEW_CODE: Add hydration for Firestore collections.
		for _, collection := range dataflow.Resources.FirestoreCollections {
			for _, producer := range collection.Producers {
				if producer.Lookup.Method != LookupEnv {
					continue
				}
				if service, ok := dataflow.Services[producer.Name]; ok {
					injectEnvVar(service.Deployment, producer.Lookup, collection.Name, "WRITE_COLLECTION")
				}
			}
			for _, consumer := range collection.Consumers {
				if consumer.Lookup.Method != LookupEnv {
					continue
				}
				if service, ok := dataflow.Services[consumer.Name]; ok {
					injectEnvVar(service.Deployment, consumer.Lookup, collection.Name, "READ_COLLECTION")
				}
			}
		}
	}
}

func updateAllCrossReferences(dataflow *ResourceGroup, runID string) {
	for _, service := range dataflow.Services {
		for i, depName := range service.Dependencies {
			service.Dependencies[i] = fmt.Sprintf("%s-%s", depName, runID)
		}
	}
	resources := &dataflow.Resources
	for i := range resources.Topics {
		if resources.Topics[i].ProducerService != nil {
			resources.Topics[i].ProducerService.Name = fmt.Sprintf("%s-%s", resources.Topics[i].ProducerService.Name, runID)
		}
		for j := range resources.Topics[i].IAMPolicy {
			resources.Topics[i].IAMPolicy[j].Name = fmt.Sprintf("%s-%s", resources.Topics[i].IAMPolicy[j].Name, runID)
		}
	}
	for i := range resources.Subscriptions {
		if resources.Subscriptions[i].ConsumerService != nil {
			resources.Subscriptions[i].ConsumerService.Name = fmt.Sprintf("%s-%s", resources.Subscriptions[i].ConsumerService.Name, runID)
		}
		for j := range resources.Subscriptions[i].IAMPolicy {
			resources.Subscriptions[i].IAMPolicy[j].Name = fmt.Sprintf("%s-%s", resources.Subscriptions[i].IAMPolicy[j].Name, runID)
		}
	}
	for i := range resources.GCSBuckets {
		for j := range resources.GCSBuckets[i].Producers {
			resources.GCSBuckets[i].Producers[j].Name = fmt.Sprintf("%s-%s", resources.GCSBuckets[i].Producers[j].Name, runID)
		}
		for j := range resources.GCSBuckets[i].Consumers {
			resources.GCSBuckets[i].Consumers[j].Name = fmt.Sprintf("%s-%s", resources.GCSBuckets[i].Consumers[j].Name, runID)
		}
		for j := range resources.GCSBuckets[i].IAMPolicy {
			resources.GCSBuckets[i].IAMPolicy[j].Name = fmt.Sprintf("%s-%s", resources.GCSBuckets[i].IAMPolicy[j].Name, runID)
		}
	}
	for i := range resources.BigQueryTables {
		for j := range resources.BigQueryTables[i].Producers {
			resources.BigQueryTables[i].Producers[j].Name = fmt.Sprintf("%s-%s", resources.BigQueryTables[i].Producers[j].Name, runID)
		}
		for j := range resources.BigQueryTables[i].Consumers {
			resources.BigQueryTables[i].Consumers[j].Name = fmt.Sprintf("%s-%s", resources.BigQueryTables[i].Consumers[j].Name, runID)
		}
	}
	for i := range resources.BigQueryDatasets {
		for j := range resources.BigQueryDatasets[i].IAMPolicy {
			resources.BigQueryDatasets[i].IAMPolicy[j].Name = fmt.Sprintf("%s-%s", resources.BigQueryDatasets[i].IAMPolicy[j].Name, runID)
		}
	}
}

func hydrateResourceNamesWithRunID(resources *CloudResourcesSpec, runID string, nameMap map[string]string) {
	for i := range resources.Topics {
		originalName := resources.Topics[i].Name
		hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
		resources.Topics[i].Name = hydratedName
		nameMap[originalName] = hydratedName
	}
	for i := range resources.GCSBuckets {
		originalName := resources.GCSBuckets[i].Name
		hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
		resources.GCSBuckets[i].Name = hydratedName
		nameMap[originalName] = hydratedName
	}
	for i := range resources.BigQueryDatasets {
		originalName := resources.BigQueryDatasets[i].Name
		hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
		resources.BigQueryDatasets[i].Name = hydratedName
		nameMap[originalName] = hydratedName
	}
	for i := range resources.BigQueryTables {
		originalName := resources.BigQueryTables[i].Name
		hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
		resources.BigQueryTables[i].Name = hydratedName
		nameMap[originalName] = hydratedName
		resources.BigQueryTables[i].Dataset = fmt.Sprintf("%s-%s", resources.BigQueryTables[i].Dataset, runID)
	}
	for i := range resources.Subscriptions {
		originalName := resources.Subscriptions[i].Name
		hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
		resources.Subscriptions[i].Name = hydratedName
		nameMap[originalName] = hydratedName
		resources.Subscriptions[i].Topic = fmt.Sprintf("%s-%s", resources.Subscriptions[i].Topic, runID)
	}
}

func generateImagePath(spec *DeploymentSpec, serviceName, projectID string) string {
	return fmt.Sprintf(
		"%s-docker.pkg.dev/%s/%s/%s:%s",
		spec.Region, projectID, spec.ImageRepo, serviceName, uuid.New().String()[:8],
	)
}

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
	spec.EnvironmentVars["PROJECT_ID"] = projectID
	spec.EnvironmentVars["SERVICE_NAME"] = serviceName
	spec.EnvironmentVars["DATAFLOW_NAME"] = dataflowName
	spec.Image = generateImagePath(spec, serviceName, projectID)
}

// REFACTOR: This function now accepts the Lookup struct. For now, it only uses the
// 'Key' field to support the environment variable injection pattern. It can be
// extended in the future to handle the 'Method' field.
func injectEnvVar(spec *DeploymentSpec, lookup Lookup, resourceName, keySuffix string) {
	if spec.EnvironmentVars == nil {
		spec.EnvironmentVars = make(map[string]string)
	}
	envKey := lookup.Key
	if envKey == "" {
		envKey = fmt.Sprintf("%s_%s", strings.Replace(strings.ToUpper(resourceName), "-", "_", -1), keySuffix)
	}
	spec.EnvironmentVars[envKey] = resourceName
}

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
