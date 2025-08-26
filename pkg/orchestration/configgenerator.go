package orchestration

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

// isLinkedForYAML checks if a service is linked to a resource with the intent
// of using the embedded YAML configuration method.
func isLinkedForYAML(links []servicemanager.ServiceMapping, serviceName string) bool {
	for _, link := range links {
		if link.Name == serviceName {
			if link.Lookup.Method == "" || link.Lookup.Method == servicemanager.LookupYAML {
				return true
			}
		}
	}
	return false
}

// ReadResourceMappings extract the lookup names from a CloudResourcesSpec.
func ReadResourceMappings(spec *servicemanager.CloudResourcesSpec) map[string]string {
	lookupMap := make(map[string]string)

	mr := func(rio servicemanager.ResourceIO) {
		for _, consumer := range rio.Consumers {
			if consumer.Lookup.Key != "" {
				lookupMap[consumer.Lookup.Key] = consumer.Name
			}
		}
		for _, producer := range rio.Producers {
			if producer.Lookup.Key != "" {
				lookupMap[producer.Lookup.Key] = producer.Name
			}
		}
	}

	for _, topic := range spec.Topics {
		if topic.ProducerService != nil && topic.ProducerService.Lookup.Key != "" {
			lookupMap[topic.ProducerService.Lookup.Key] = topic.Name
		}
	}
	for _, sub := range spec.Subscriptions {
		if sub.ConsumerService != nil && sub.ConsumerService.Lookup.Key != "" {
			lookupMap[sub.ConsumerService.Lookup.Key] = sub.Name
		}
	}
	for _, table := range spec.BigQueryTables {
		mr(table.ResourceIO)
	}
	for _, collection := range spec.FirestoreCollections {
		mr(collection.ResourceIO)
	}
	for _, bucket := range spec.GCSBuckets {
		mr(bucket.ResourceIO)
	}
	return lookupMap
}

// buildServiceResourceSpec creates the filtered CloudResourcesSpec for a single service.
func buildServiceResourceSpec(service servicemanager.ServiceSpec, arch *servicemanager.MicroserviceArchitecture) servicemanager.CloudResourcesSpec {
	spec := servicemanager.CloudResourcesSpec{}

	// REFACTOR: Create a lookup map of all datasets for efficient access.
	allDatasets := make(map[string]servicemanager.BigQueryDataset)
	for _, df := range arch.Dataflows {
		for _, ds := range df.Resources.BigQueryDatasets {
			allDatasets[ds.Name] = ds
		}
	}
	// REFACTOR: Use a map to collect required datasets to avoid duplicates.
	datasetsToAdd := make(map[string]servicemanager.BigQueryDataset)

	for _, dataflow := range arch.Dataflows {
		// Check Topics
		for _, topic := range dataflow.Resources.Topics {
			if topic.ProducerService != nil && isLinkedForYAML([]servicemanager.ServiceMapping{*topic.ProducerService}, service.Name) {
				spec.Topics = append(spec.Topics, topic)
			}
		}
		// Check Subscriptions
		for _, sub := range dataflow.Resources.Subscriptions {
			if sub.ConsumerService != nil && isLinkedForYAML([]servicemanager.ServiceMapping{*sub.ConsumerService}, service.Name) {
				spec.Subscriptions = append(spec.Subscriptions, sub)
			}
		}
		// Check BigQuery Tables and infer Dataset dependency
		for _, table := range dataflow.Resources.BigQueryTables {
			if isLinkedForYAML(table.Producers, service.Name) || isLinkedForYAML(table.Consumers, service.Name) {
				// 1. Add the linked table to the service's spec.
				spec.BigQueryTables = append(spec.BigQueryTables, table)

				// 2. Infer the dependency on the parent dataset and add it to our collection.
				if parentDataset, ok := allDatasets[table.Dataset]; ok {
					datasetsToAdd[parentDataset.Name] = parentDataset
				}
			}
		}
		// Check GCS Buckets
		for _, bucket := range dataflow.Resources.GCSBuckets {
			if isLinkedForYAML(bucket.Producers, service.Name) || isLinkedForYAML(bucket.Consumers, service.Name) {
				spec.GCSBuckets = append(spec.GCSBuckets, bucket)
			}
		}
		// Check Firestore Databases
		for _, db := range dataflow.Resources.FirestoreDatabases {
			if isLinkedForYAML(db.Producers, service.Name) || isLinkedForYAML(db.Consumers, service.Name) {
				spec.FirestoreDatabases = append(spec.FirestoreDatabases, db)
			}
		}
		// Check Firestore Collections
		for _, coll := range dataflow.Resources.FirestoreCollections {
			if isLinkedForYAML(coll.Producers, service.Name) || isLinkedForYAML(coll.Consumers, service.Name) {
				spec.FirestoreCollections = append(spec.FirestoreCollections, coll)
			}
		}
	}

	// REFACTOR: Add the unique, transitively-linked datasets to the final spec.
	for _, ds := range datasetsToAdd {
		spec.BigQueryDatasets = append(spec.BigQueryDatasets, ds)
	}

	return spec
}

// GenerateServiceConfigs creates service-specific YAML configs based on the architecture.
func GenerateServiceConfigs(arch *servicemanager.MicroserviceArchitecture) ([]ServiceConfig, error) {

	var configFiles []ServiceConfig
	allServices := getAllServices(arch)

	for serviceName, service := range allServices {
		if service.Deployment == nil {
			continue // Skip services without a deployment spec
		}
		serviceResourceSpec := buildServiceResourceSpec(service, arch)

		destPath := filepath.Join(service.Deployment.SourcePath, service.Deployment.BuildableModulePath, "resources.yaml")
		configFiles = append(configFiles, ServiceConfig{
			ServiceName: serviceName,
			FilePath:    destPath,
			Config:      serviceResourceSpec,
		})
	}
	return configFiles, nil
}

// getAllServices is a local helper to collect all service specifications into a single map.
func getAllServices(arch *servicemanager.MicroserviceArchitecture) map[string]servicemanager.ServiceSpec {
	allServices := make(map[string]servicemanager.ServiceSpec)

	if arch.ServiceManagerSpec.Name != "" {
		allServices[arch.ServiceManagerSpec.Name] = arch.ServiceManagerSpec.ServiceSpec
	}

	for _, dataflow := range arch.Dataflows {
		for _, service := range dataflow.Services {
			allServices[service.Name] = service
		}
	}
	return allServices
}

type ServiceConfig struct {
	ServiceName string
	FilePath    string
	Config      servicemanager.CloudResourcesSpec
}

// WriteServiceConfigFiles writes the generated configurations to their respective service directories.
func WriteServiceConfigFiles(
	filesToWrite []ServiceConfig,
	logger zerolog.Logger,
) error {
	logger.Info().Msg("Writing service-specific config files...")

	for _, spec := range filesToWrite {

		yamlBytes, err := yaml.Marshal(&spec.Config)
		if err != nil {
			return fmt.Errorf("failed to marshal config for service '%s': %w", spec.ServiceName, err)
		}

		logger.Debug().Str("destination", spec.FilePath).Msgf("Writing config for '%s'", spec.ServiceName)
		err = os.WriteFile(spec.FilePath, yamlBytes, 0644)
		if err != nil {
			return fmt.Errorf("failed to write resources.yaml for service %s: %w", spec.ServiceName, err)
		}
	}

	logger.Info().Msg("✅ All service config files written successfully.")
	return nil
}
