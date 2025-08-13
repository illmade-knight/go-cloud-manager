package orchestration

import (
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGenerateServiceConfigs(t *testing.T) {
	// --- Arrange ---
	// 1. Create a mock architecture with multiple services and resources.
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			ProjectID: "test-project",
			Region:    "test-region",
		},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           "service-director",
			ServiceAccount: "sd-sa",
			Deployment: &servicemanager.DeploymentSpec{
				SourcePath: ".",
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"publisher-service": {
						Name:           "publisher-service",
						ServiceAccount: "pub-sa",
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath: ".",
						},
					},
					"subscriber-service": {
						Name:           "subscriber-service",
						ServiceAccount: "sub-sa",
						Deployment: &servicemanager.DeploymentSpec{
							SourcePath: ".",
						},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "topic-a"},
							ProducerService: &servicemanager.ServiceMapping{Name: "publisher-service"},
						},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "sub-a"},
							Topic:           "topic-a",
							ConsumerService: &servicemanager.ServiceMapping{Name: "subscriber-service"},
						},
					},
				},
			},
		},
	}

	// --- Act ---
	// 2. Call the helper function to generate the configs.
	serviceConfigs, err := GenerateServiceConfigs(arch, "test-repo", zerolog.Nop())

	// --- Assert ---
	// 3. Verify the results.
	require.NoError(t, err)
	require.Len(t, serviceConfigs, 3, "Should generate a config for all 3 services")

	// 4. Check the publisher's config.
	publisherYAML, ok := serviceConfigs["publisher-service"]
	require.True(t, ok)

	var publisherWrapper struct {
		Resources servicemanager.CloudResourcesSpec `yaml:"resources"`
	}
	err = yaml.Unmarshal(publisherYAML, &publisherWrapper)
	require.NoError(t, err)
	require.Len(t, publisherWrapper.Resources.Topics, 1, "Publisher should have one topic")
	require.Equal(t, "topic-a", publisherWrapper.Resources.Topics[0].Name)
	require.Empty(t, publisherWrapper.Resources.Subscriptions, "Publisher should have no subscriptions")

	// 5. Check the subscriber's config.
	subscriberYAML, ok := serviceConfigs["subscriber-service"]
	require.True(t, ok)

	var subscriberWrapper struct {
		Resources servicemanager.CloudResourcesSpec `yaml:"resources"`
	}
	err = yaml.Unmarshal(subscriberYAML, &subscriberWrapper)
	require.NoError(t, err)
	require.Len(t, subscriberWrapper.Resources.Subscriptions, 1, "Subscriber should have one subscription")
	require.Equal(t, "sub-a", subscriberWrapper.Resources.Subscriptions[0].Name)
	require.Empty(t, subscriberWrapper.Resources.Topics, "Subscriber should have no topics")

	// 6. Check the ServiceDirector's config (it has no direct resource links).
	directorYAML, ok := serviceConfigs["service-director"]
	require.True(t, ok)

	var directorWrapper struct {
		Resources servicemanager.CloudResourcesSpec `yaml:"resources"`
	}
	err = yaml.Unmarshal(directorYAML, &directorWrapper)
	require.NoError(t, err)
	require.Empty(t, directorWrapper.Resources.Topics, "Director should have no topics")
	require.Empty(t, directorWrapper.Resources.Subscriptions, "Director should have no subscriptions")
}
