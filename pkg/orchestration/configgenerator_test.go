package orchestration_test

import (
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/stretchr/testify/require"
)

// findConfigForService is a test-local helper to make assertions cleaner.
// It iterates through the result slice and finds the config for a specific service.
func findConfigForService(t *testing.T, configs []orchestration.ServiceConfig, serviceName string) servicemanager.CloudResourcesSpec {
	t.Helper()
	for _, c := range configs {
		if c.ServiceName == serviceName {
			return c.Config
		}
	}
	require.Fail(t, "config not found for service", serviceName)
	return servicemanager.CloudResourcesSpec{}
}

func TestGenerateServiceConfigs_DefaultToYAML(t *testing.T) {
	// --- Arrange ---
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: "test-project", Region: "test-region"},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "service-director", ServiceAccount: "sd-sa",
			Deployment: &servicemanager.DeploymentSpec{SourcePath: "."},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"publisher-service": {
						Name: "publisher-service", ServiceAccount: "pub-sa",
						Deployment: &servicemanager.DeploymentSpec{SourcePath: "."},
					},
					"subscriber-service": {
						Name: "subscriber-service", ServiceAccount: "sub-sa",
						Deployment: &servicemanager.DeploymentSpec{SourcePath: "."},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource: servicemanager.CloudResource{Name: "env-var-topic"},
							ProducerService: &servicemanager.ServiceMapping{
								Name:   "publisher-service",
								Lookup: servicemanager.Lookup{Method: "env_var"}, // Explicitly env_var
							},
						},
						{
							CloudResource: servicemanager.CloudResource{Name: "default-topic"},
							ProducerService: &servicemanager.ServiceMapping{
								Name: "publisher-service",
								// This lookup is empty, so it should default to embedded_yaml
								Lookup: servicemanager.Lookup{},
							},
						},
					},
				},
			},
		},
	}

	// --- Act ---
	// Call the helper function from the 'orchestration' package to generate the configs.
	configFiles, err := orchestration.GenerateServiceConfigs(arch)

	// --- Assert ---
	require.NoError(t, err)

	// Use the test helper to find and validate the publisher's config.
	publisherCfg := findConfigForService(t, configFiles, "publisher-service")

	// It should find ONLY the topic where the method is empty (our new default).
	require.Len(t, publisherCfg.Topics, 1, "Publisher should have one topic for the YAML config")
	require.Equal(t, "default-topic", publisherCfg.Topics[0].Name, "The topic with the empty lookup method should be present")

	// The subscriber has no links, so its config should be empty.
	subscriberCfg := findConfigForService(t, configFiles, "subscriber-service")
	require.Empty(t, subscriberCfg.Topics, "Subscriber should have no topics in its YAML")
}
