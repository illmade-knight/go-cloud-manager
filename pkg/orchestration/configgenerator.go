package orchestration

import (
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

// getAllServices is a local helper to collect all service specifications from
// an architecture, including the ServiceDirector, into a single map.
func getAllServices(arch *servicemanager.MicroserviceArchitecture) map[string]servicemanager.ServiceSpec {
	allServices := make(map[string]servicemanager.ServiceSpec)

	// Add the ServiceDirector itself
	if arch.ServiceManagerSpec.Name != "" {
		allServices[arch.ServiceManagerSpec.Name] = arch.ServiceManagerSpec
	}

	// Add all services from all dataflows
	for _, dataflow := range arch.Dataflows {
		for _, service := range dataflow.Services {
			allServices[service.Name] = service
		}
	}
	return allServices
}

// GenerateServiceConfigs hydrates the full architecture and then creates a
// map of service-specific, subset YAML configurations.
// It returns a map where the key is the service name and the value is the
// marshaled YAML []byte for that service's configuration.
func GenerateServiceConfigs(
	arch *servicemanager.MicroserviceArchitecture,
	imageRepo string,
	logger zerolog.Logger,
) (map[string][]byte, error) {

	// 1. Hydrate the entire architecture for the production run.
	err := servicemanager.HydrateArchitecture(arch, imageRepo, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to hydrate architecture: %w", err)
	}

	serviceConfigs := make(map[string][]byte)
	allServices := getAllServices(arch)

	// 2. Iterate through each service to generate its specific config.
	for serviceName, service := range allServices {
		serviceResourceSpec := servicemanager.CloudResourcesSpec{}

		// Find all resources this service produces or consumes across all dataflows.
		for _, dataflow := range arch.Dataflows {
			// --- Pub/Sub Topics ---
			for _, topic := range dataflow.Resources.Topics {
				if topic.ProducerService != nil && topic.ProducerService.Name == service.Name {
					serviceResourceSpec.Topics = append(serviceResourceSpec.Topics, topic)
				}
			}
			// --- Pub/Sub Subscriptions ---
			for _, sub := range dataflow.Resources.Subscriptions {
				if sub.ConsumerService != nil && sub.ConsumerService.Name == service.Name {
					serviceResourceSpec.Subscriptions = append(serviceResourceSpec.Subscriptions, sub)
				}
			}
			// --- BigQuery Tables ---
			for _, table := range dataflow.Resources.BigQueryTables {
				isLinked := false
				for _, producer := range table.Producers {
					if producer.Name == service.Name {
						isLinked = true
						break
					}
				}
				if !isLinked {
					for _, consumer := range table.Consumers {
						if consumer.Name == service.Name {
							isLinked = true
							break
						}
					}
				}
				if isLinked {
					serviceResourceSpec.BigQueryTables = append(serviceResourceSpec.BigQueryTables, table)
				}
			}
			// --- GCS Buckets (Previously missing logic) ---
			for _, bucket := range dataflow.Resources.GCSBuckets {
				isLinked := false
				for _, producer := range bucket.Producers {
					if producer.Name == service.Name {
						isLinked = true
						break
					}
				}
				if !isLinked {
					for _, consumer := range bucket.Consumers {
						if consumer.Name == service.Name {
							isLinked = true
							break
						}
					}
				}
				if isLinked {
					serviceResourceSpec.GCSBuckets = append(serviceResourceSpec.GCSBuckets, bucket)
				}
			}
			// --- Firestore Collections (Previously missing logic) ---
			for _, collection := range dataflow.Resources.FirestoreCollections {
				isLinked := false
				for _, producer := range collection.Producers {
					if producer.Name == service.Name {
						isLinked = true
						break
					}
				}
				if !isLinked {
					for _, consumer := range collection.Consumers {
						if consumer.Name == service.Name {
							isLinked = true
							break
						}
					}
				}
				if isLinked {
					serviceResourceSpec.FirestoreCollections = append(serviceResourceSpec.FirestoreCollections, collection)
				}
			}
		}

		// 3. Marshal the subset of resources into YAML.
		// The wrapper ensures the top-level key is "resources:".
		wrapper := struct {
			Resources servicemanager.CloudResourcesSpec `yaml:"resources"`
		}{Resources: serviceResourceSpec}

		yamlBytes, err := yaml.Marshal(&wrapper)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal config for service '%s': %w", serviceName, err)
		}
		serviceConfigs[serviceName] = yamlBytes
	}

	return serviceConfigs, nil
}
