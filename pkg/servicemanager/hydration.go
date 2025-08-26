package servicemanager

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// HydrateArchitecture prepares an architecture for a production-like deployment.
func HydrateArchitecture(arch *MicroserviceArchitecture, defaultImageRepo string, logger zerolog.Logger) error {
	if arch == nil {
		return errors.New("cannot hydrate a nil architecture")
	}
	if err := arch.Validate(); err != nil {
		return fmt.Errorf("architecture validation failed: %w", err)
	}

	log := logger.With().Str("component", "Hydration").Logger()
	log.Info().Msg("Starting production architecture hydration...")

	generateAndInjectServiceDirectorResources(arch)
	applyDefaults(arch)
	// REFACTOR: These two steps now work together to ensure datasets are present and linked.
	generateDatasetsFromTableRefs(arch)

	hydrateAllDeploymentSpecs(arch, defaultImageRepo)
	injectAllEnvironmentVariables(arch)

	log.Info().Msg("Production architecture hydration complete.")
	return nil
}

// HydrateTestArchitecture prepares an architecture for an isolated test run.
func HydrateTestArchitecture(arch *MicroserviceArchitecture, defaultImageRepo, runID string, logger zerolog.Logger) (map[string]string, error) {
	if runID == "" {
		return nil, errors.New("HydrateTestArchitecture requires a non-empty runID")
	}
	if arch == nil {
		return nil, errors.New("cannot hydrate a nil architecture")
	}
	if err := arch.Validate(); err != nil {
		return nil, fmt.Errorf("architecture validation failed: %w", err)
	}

	log := logger.With().Str("component", "Hydration").Logger()
	log.Info().Msgf("Starting test architecture hydration with runID '%s'...", runID)

	nameMap := make(map[string]string)

	generateAndInjectServiceDirectorResources(arch)
	applyDefaults(arch)
	// REFACTOR: These two steps now work together to ensure datasets are present and linked.
	generateDatasetsFromTableRefs(arch)

	applyRunIDTransformation(arch, runID, nameMap)
	hydrateAllDeploymentSpecs(arch, defaultImageRepo)
	injectAllEnvironmentVariables(arch)

	log.Info().Msg("Test architecture hydration complete.")
	return nameMap, nil
}

// --- Private Helpers ---

// REFACTOR: This new function automatically creates dataset definitions based on
// references from table definitions, improving the DRY principle in services.yaml.
func generateDatasetsFromTableRefs(arch *MicroserviceArchitecture) {
	// 1. Get a set of all currently defined datasets.
	existingDatasets := make(map[string]bool)
	for _, df := range arch.Dataflows {
		for _, ds := range df.Resources.BigQueryDatasets {
			existingDatasets[ds.Name] = true
		}
	}

	// 2. Iterate through all tables and find their parent datasets.
	for dfName, dataflow := range arch.Dataflows {
		for _, table := range dataflow.Resources.BigQueryTables {
			datasetName := table.Dataset
			// 3. If a table references a dataset that is not explicitly defined, create it.
			if datasetName != "" && !existingDatasets[datasetName] {
				newDataset := BigQueryDataset{
					CloudResource: CloudResource{
						Name: datasetName,
					},
					Location: arch.Environment.Location, // Use the default location from the top-level environment.
				}
				// Add the new dataset to the same dataflow where the table was found.
				df := arch.Dataflows[dfName]
				df.Resources.BigQueryDatasets = append(df.Resources.BigQueryDatasets, newDataset)
				arch.Dataflows[dfName] = df

				// Mark it as "existing" now to prevent duplicates.
				existingDatasets[datasetName] = true
			}
		}
	}
}

// generateAndInjectServiceDirectorResources creates the Director's own resources.
func generateAndInjectServiceDirectorResources(arch *MicroserviceArchitecture) {
	spec := arch.ServiceManagerSpec
	if spec.CommandTopic.Name == "" || spec.CompletionTopic.Name == "" {
		return
	}

	cmdTopicName := spec.CommandTopic.Name
	compTopicName := spec.CompletionTopic.Name
	cmdSubName := fmt.Sprintf("%s-sub", cmdTopicName)

	cmdTopic := TopicConfig{
		CloudResource: CloudResource{Name: cmdTopicName},
	}

	if spec.CompletionTopic.Lookup.Key == "" {
		spec.CompletionTopic.Lookup.Key = fmt.Sprintf("%s-id", spec.Name)
	}
	compTopic := TopicConfig{
		CloudResource: CloudResource{Name: compTopicName},
		ProducerService: &ServiceMapping{
			Name:   spec.Name,
			Lookup: spec.CompletionTopic.Lookup,
		},
	}

	cmdSub := SubscriptionConfig{
		CloudResource: CloudResource{Name: cmdSubName},
		Topic:         cmdTopicName,
		ConsumerService: &ServiceMapping{
			Name: spec.Name,
			Lookup: Lookup{
				Key:    "command-subscription-id",
				Method: spec.CommandTopic.Lookup.Method,
			},
		},
	}

	directorResourceFlow := ResourceGroup{
		Name:        fmt.Sprintf("%s-infra", spec.Name),
		Description: fmt.Sprintf("Auto-generated infrastructure resources for the ServiceDirector (%s).", spec.Name),
		Resources: CloudResourcesSpec{
			Topics:        []TopicConfig{cmdTopic, compTopic},
			Subscriptions: []SubscriptionConfig{cmdSub},
		},
		Lifecycle: &LifecyclePolicy{Strategy: LifecycleStrategyPermanent},
	}

	if arch.Dataflows == nil {
		arch.Dataflows = make(map[string]ResourceGroup)
	}
	arch.Dataflows[directorResourceFlow.Name] = directorResourceFlow
}

// applyRunIDTransformation modifies all names and links with a unique test run ID.
func applyRunIDTransformation(arch *MicroserviceArchitecture, runID string, nameMap map[string]string) {
	if arch.ServiceManagerSpec.Deployment != nil {
		originalName := arch.ServiceManagerSpec.Name
		hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
		arch.ServiceManagerSpec.Name = hydratedName
		arch.ServiceManagerSpec.ServiceAccount = fmt.Sprintf("%s-%s", arch.ServiceManagerSpec.ServiceAccount, runID)
		nameMap[originalName] = hydratedName
	}

	arch.ServiceManagerSpec.CommandTopic.Name = fmt.Sprintf("%s-%s", arch.ServiceManagerSpec.CommandTopic.Name, runID)
	arch.ServiceManagerSpec.CompletionTopic.Name = fmt.Sprintf("%s-%s", arch.ServiceManagerSpec.CompletionTopic.Name, runID)

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

		for i, job := range dataflow.Resources.CloudSchedulerJobs {
			dataflow.Resources.CloudSchedulerJobs[i].ServiceAccount = fmt.Sprintf("%s-%s", job.ServiceAccount, runID)
		}

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

// applyDefaults populates optional fields with sensible defaults.
func applyDefaults(arch *MicroserviceArchitecture) {
	for _, dataflow := range arch.Dataflows {
		for i, job := range dataflow.Resources.CloudSchedulerJobs {
			if job.ServiceAccount == "" {
				defaultSA := fmt.Sprintf("%s-sa", job.Name)
				dataflow.Resources.CloudSchedulerJobs[i].ServiceAccount = defaultSA
			}
		}
	}
}

// injectAllEnvironmentVariables handles the injection of all resource links as env vars.
func injectAllEnvironmentVariables(arch *MicroserviceArchitecture) {
	for _, dataflow := range arch.Dataflows {
		for _, topic := range dataflow.Resources.Topics {
			if topic.ProducerService != nil && topic.ProducerService.Lookup.Method == LookupEnv {
				var targetService *ServiceSpec
				if topic.ProducerService.Name == arch.ServiceManagerSpec.Name {
					targetService = &arch.ServiceManagerSpec.ServiceSpec
				} else if service, ok := dataflow.Services[topic.ProducerService.Name]; ok {
					targetService = &service
				}

				if targetService != nil && targetService.Deployment != nil {
					injectEnvVar(targetService.Deployment, topic.ProducerService.Lookup, topic.Name, "TOPIC_ID")
				}
			}
		}
		for _, sub := range dataflow.Resources.Subscriptions {
			if sub.ConsumerService != nil && sub.ConsumerService.Lookup.Method == LookupEnv {
				var targetService *ServiceSpec
				if sub.ConsumerService.Name == arch.ServiceManagerSpec.Name {
					targetService = &arch.ServiceManagerSpec.ServiceSpec
				} else if service, ok := dataflow.Services[sub.ConsumerService.Name]; ok {
					targetService = &service
				}

				if targetService != nil && targetService.Deployment != nil {
					injectEnvVar(targetService.Deployment, sub.ConsumerService.Lookup, sub.Name, "SUB_ID")
				}
			}
		}
		for _, table := range dataflow.Resources.BigQueryTables {
			for _, producer := range table.Producers {
				if producer.Lookup.Method == LookupEnv {
					if service, ok := dataflow.Services[producer.Name]; ok {
						injectEnvVar(service.Deployment, producer.Lookup, table.Name, "WRITE_TABLE")
					}
				}
			}
			for _, consumer := range table.Consumers {
				if consumer.Lookup.Method == LookupEnv {
					if service, ok := dataflow.Services[consumer.Name]; ok {
						injectEnvVar(service.Deployment, consumer.Lookup, table.Name, "READ_TABLE")
					}
				}
			}
		}
		for _, bucket := range dataflow.Resources.GCSBuckets {
			for _, producer := range bucket.Producers {
				if producer.Lookup.Method == LookupEnv {
					if service, ok := dataflow.Services[producer.Name]; ok {
						injectEnvVar(service.Deployment, producer.Lookup, bucket.Name, "WRITE_BUCKET")
					}
				}
			}
			for _, consumer := range bucket.Consumers {
				if consumer.Lookup.Method == LookupEnv {
					if service, ok := dataflow.Services[consumer.Name]; ok {
						injectEnvVar(service.Deployment, consumer.Lookup, bucket.Name, "READ_BUCKET")
					}
				}
			}
		}
		for _, collection := range dataflow.Resources.FirestoreCollections {
			for _, producer := range collection.Producers {
				if producer.Lookup.Method == LookupEnv {
					if service, ok := dataflow.Services[producer.Name]; ok {
						injectEnvVar(service.Deployment, producer.Lookup, collection.Name, "WRITE_COLLECTION")
					}
				}
			}
			for _, consumer := range collection.Consumers {
				if consumer.Lookup.Method == LookupEnv {
					if service, ok := dataflow.Services[consumer.Name]; ok {
						injectEnvVar(service.Deployment, consumer.Lookup, collection.Name, "READ_COLLECTION")
					}
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
	for i, job := range resources.CloudSchedulerJobs {
		if job.TargetService != "" {
			resources.CloudSchedulerJobs[i].TargetService = fmt.Sprintf("%s-%s", job.TargetService, runID)
		}
	}

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
		resources.Subscriptions[i].Topic = fmt.Sprintf("%s-%s", resources.Subscriptions[i].Topic, runID)
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
	for i := range resources.CloudSchedulerJobs {
		originalName := resources.CloudSchedulerJobs[i].Name
		hydratedName := fmt.Sprintf("%s-%s", originalName, runID)
		resources.CloudSchedulerJobs[i].Name = hydratedName
		nameMap[originalName] = hydratedName
	}
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
