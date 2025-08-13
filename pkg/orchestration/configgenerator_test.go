package orchestration

import (
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
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
							CloudResource:   servicemanager.CloudResource{Name: "topic"},
							ProducerService: &servicemanager.ServiceMapping{Name: "publisher-service"},
						},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "sub"},
							Topic:           "topic",
							ConsumerService: &servicemanager.ServiceMapping{Name: "subscriber-service"},
						},
					},
				},
			},
		},
	}

	_, err := servicemanager.HydrateTestArchitecture(arch, "test_repo", "a", zerolog.Nop())
	require.NoError(t, err)

	// --- Act ---
	// 2. Call the helper function to generate the configs.
	serviceConfigs, err := GenerateServiceConfigs(arch, false)

	// --- Assert ---
	// 3. Verify the results.
	require.NoError(t, err)
	require.Len(t, serviceConfigs, 3, "Should generate a config for all 3 services")

	// 4. Check the publisher's config.
	publisherWrapper, ok := serviceConfigs["publisher-service-a"]

	require.NoError(t, err)
	require.Len(t, publisherWrapper.Topics, 1, "Publisher should have one topic")
	require.Equal(t, "topic-a", publisherWrapper.Topics[0].Name)
	require.Empty(t, publisherWrapper.Subscriptions, "Publisher should have no subscriptions")

	// 5. Check the subscriber's config.
	subscriberWrapper, ok := serviceConfigs["subscriber-service-a"]
	require.NoError(t, err)
	require.Len(t, subscriberWrapper.Subscriptions, 1, "Subscriber should have one subscription")
	require.Equal(t, "sub-a", subscriberWrapper.Subscriptions[0].Name)
	require.Empty(t, subscriberWrapper.Topics, "Subscriber should have no topics")

	// 6. Check the ServiceDirector's config (it has no direct resource links).
	directorWrapper, ok := serviceConfigs["service-director-a"]
	require.True(t, ok)

	require.Empty(t, directorWrapper.Topics, "Director should have no topics")
	require.Empty(t, directorWrapper.Subscriptions, "Director should have no subscriptions")
}
