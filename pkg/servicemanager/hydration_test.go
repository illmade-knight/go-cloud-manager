package servicemanager_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHydrateArchitecture_DefaultsAndImage asserts that default values for CPU, memory, region,
// and a dynamic image path are correctly applied to a service's deployment spec.
func TestHydrateArchitecture_DefaultsAndImage(t *testing.T) {
	// ARRANGE
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
					"my-service": {
						Name:           "my-service",
						ServiceAccount: "my-sa",
						Deployment:     &servicemanager.DeploymentSpec{SourcePath: "./cmd/myservice"},
					},
				},
			},
		},
	}
	logger := zerolog.Nop()

	// ACT
	err := servicemanager.HydrateArchitecture(arch, "default-repo", "", logger)
	require.NoError(t, err)

	// ASSERT
	hydratedSvc := arch.Dataflows["test-flow"].Services["my-service"]
	require.NotNil(t, hydratedSvc.Deployment)
	spec := hydratedSvc.Deployment

	assert.Equal(t, "europe-west1", spec.Region, "Region should be hydrated from the environment default")
	assert.Equal(t, "1", spec.CPU, "CPU should be hydrated with the default value")
	assert.Equal(t, "512Mi", spec.Memory, "Memory should be hydrated with the default value")

	// Assert the image path has the correct structure.
	expectedImagePrefix := "europe-west1-docker.pkg.dev/test-project/default-repo/my-service:"
	assert.True(t, strings.HasPrefix(spec.Image, expectedImagePrefix), "Image path prefix is incorrect")
	assert.Len(t, spec.Image, len(expectedImagePrefix)+8, "Image tag should be an 8-character UUID")
}

// TestHydrateArchitecture_WithRunID asserts that a provided runID is correctly appended
// to the names of the service manager and all services in dataflows.
func TestHydrateArchitecture_WithRunID(t *testing.T) {
	// ARRANGE
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: "test-project", Region: "us-central1"},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name:           "service-manager",
			ServiceAccount: "sm-sa",
			Deployment:     &servicemanager.DeploymentSpec{SourcePath: "."},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"service-a": {
						Name:           "service-a",
						ServiceAccount: "sa-a",
						Deployment:     &servicemanager.DeploymentSpec{SourcePath: "./cmd/svca"},
					},
				},
			},
		},
	}
	logger := zerolog.Nop()
	runID := "xyz123"

	// ACT
	err := servicemanager.HydrateArchitecture(arch, "default-repo", runID, logger)
	require.NoError(t, err)

	// ASSERT
	expectedSmName := fmt.Sprintf("service-manager-%s", runID)
	assert.Equal(t, expectedSmName, arch.ServiceManagerSpec.Name)

	hydratedSvc := arch.Dataflows["test-flow"].Services["service-a"]
	expectedSvcName := fmt.Sprintf("service-a-%s", runID)
	assert.Equal(t, expectedSvcName, hydratedSvc.Name)

	// Also assert that the hydrated name is used in the image path.
	assert.Contains(t, hydratedSvc.Deployment.Image, "/"+expectedSvcName+":")
}

// TestHydrateArchitecture_PubSubEnvInjection asserts that environment variables for
// Pub/Sub topics and subscriptions are correctly injected into the services
// that are explicitly linked to them.
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
	logger := zerolog.Nop()

	// ACT: Run the hydration logic.
	err := servicemanager.HydrateArchitecture(arch, "default-repo", "", logger)
	require.NoError(t, err)

	// ASSERT: Check that the correct environment variables were injected.
	producerSvc := arch.Dataflows["test-flow"].Services["producer-service"]
	consumerSvc := arch.Dataflows["test-flow"].Services["consumer-service"]

	require.NotNil(t, producerSvc.Deployment.EnvironmentVars)
	require.NotNil(t, consumerSvc.Deployment.EnvironmentVars)

	// Check the producer service for the topic ID.
	expectedTopicKey := "DATA-TOPIC_TOPIC_ID"
	actualTopicID, ok := producerSvc.Deployment.EnvironmentVars[expectedTopicKey]
	assert.True(t, ok, "Producer service should have the topic ID env var")
	assert.Equal(t, "data-topic", actualTopicID)

	// Check the consumer service for the subscription ID.
	expectedSubKey := "DATA-SUB_SUBSCRIPTION_ID"
	actualSubID, ok := consumerSvc.Deployment.EnvironmentVars[expectedSubKey]
	assert.True(t, ok, "Consumer service should have the subscription ID env var")
	assert.Equal(t, "data-sub", actualSubID)
}

// TestHydrateArchitecture_ValidationFailure asserts that hydration fails if the
// initial architecture does not pass validation.
func TestHydrateArchitecture_ValidationFailure(t *testing.T) {
	// ARRANGE: Create an invalid architecture (missing a required field).
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			ProjectID: "test-project",
			Region:    "europe-west1",
		},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "service-manager",
			// ServiceAccount is missing.
		},
	}
	logger := zerolog.Nop()

	// ACT
	err := servicemanager.HydrateArchitecture(arch, "default-repo", "", logger)

	// ASSERT
	require.Error(t, err)
	assert.Contains(t, err.Error(), "architecture validation failed")
	assert.Contains(t, err.Error(), "ServiceManagerSpec.ServiceAccount is a required field")
}
