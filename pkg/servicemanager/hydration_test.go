package servicemanager_test

import (
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHydrateArchitecture_PubSubEnvInjection(t *testing.T) {
	// ARRANGE: Create a minimal architecture with explicit producer/consumer links.
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			ProjectID: "test-project",
			Region:    "europe-west1",
		},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           "service-manager",
			ServiceAccount: "sm-sa",
			Deployment:     &servicemanager.DeploymentSpec{SourcePath: "."},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"producer-service": {
						Name:           "producer-service",
						ServiceAccount: "producer-sa",
						Deployment:     &servicemanager.DeploymentSpec{SourcePath: "."},
					},
					"consumer-service": {
						Name:           "consumer-service",
						ServiceAccount: "consumer-sa",
						Deployment:     &servicemanager.DeploymentSpec{SourcePath: "."},
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "data-topic"},
							ProducerService: "producer-service",
						},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "data-sub"},
							Topic:           "data-topic",
							ConsumerService: "consumer-service",
						},
					},
				},
			},
		},
	}

	// ACT: Run the hydration logic.
	err := servicemanager.HydrateArchitecture(arch, "default-repo", "")
	require.NoError(t, err)

	// ASSERT: Check that the correct environment variables were injected.
	producerSvc := arch.Dataflows["test-flow"].Services["producer-service"]
	consumerSvc := arch.Dataflows["test-flow"].Services["consumer-service"]

	require.NotNil(t, producerSvc.Deployment.EnvironmentVars)
	require.NotNil(t, consumerSvc.Deployment.EnvironmentVars)

	// Check the producer service for the topic ID
	expectedTopicKey := "DATA-TOPIC_TOPIC_ID"
	actualTopicID, ok := producerSvc.Deployment.EnvironmentVars[expectedTopicKey]
	assert.True(t, ok, "Producer service should have the topic ID env var")
	assert.Equal(t, "data-topic", actualTopicID)

	// Check the consumer service for the subscription ID
	expectedSubKey := "DATA-SUB_SUBSCRIPTION_ID"
	actualSubID, ok := consumerSvc.Deployment.EnvironmentVars[expectedSubKey]
	assert.True(t, ok, "Consumer service should have the subscription ID env var")
	assert.Equal(t, "data-sub", actualSubID)
}
