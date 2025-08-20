package servicemanager_test

import (
	"strings"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHydrateArchitecture validates the production hydration path.
func TestHydrateArchitecture(t *testing.T) {
	// ARRANGE
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: "test-project", Region: "europe-west1"},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "service-manager", ServiceAccount: "sm-sa", Deployment: &servicemanager.DeploymentSpec{SourcePath: "."},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{"test-flow": {
			Services: map[string]servicemanager.ServiceSpec{
				"my-service": {Name: "my-service", ServiceAccount: "my-sa", Deployment: &servicemanager.DeploymentSpec{SourcePath: "."}},
			},
			Resources: servicemanager.CloudResourcesSpec{
				Topics: []servicemanager.TopicConfig{{
					CloudResource: servicemanager.CloudResource{Name: "events-topic"},
					ProducerService: &servicemanager.ServiceMapping{Name: "my-service", Lookup: servicemanager.Lookup{
						Key:    "EVENTS_TOPIC_ID",
						Method: servicemanager.LookupEnv,
					}},
				}},
				// NEW_CODE: Added a Firestore collection to test its hydration.
				FirestoreCollections: []servicemanager.FirestoreCollection{{
					CloudResource: servicemanager.CloudResource{Name: "users-collection"},
					ResourceIO: servicemanager.ResourceIO{
						Producers: []servicemanager.ServiceMapping{
							{Name: "my-service", Lookup: servicemanager.Lookup{
								Key:    "USERS_COLLECTION_NAME",
								Method: servicemanager.LookupEnv,
							}},
						},
					},
				}},
			},
		}},
	}

	// ACT
	err := servicemanager.HydrateArchitecture(arch, "default-repo", zerolog.Nop())
	require.NoError(t, err)

	// ASSERT
	svc := arch.Dataflows["test-flow"].Services["my-service"]
	spec := svc.Deployment
	require.NotNil(t, spec)

	assert.Equal(t, "europe-west1", spec.Region)
	assert.True(t, strings.HasPrefix(spec.Image, "europe-west1-docker.pkg.dev/test-project/default-repo/my-service:"))
	assert.Equal(t, "events-topic", spec.EnvironmentVars["EVENTS_TOPIC_ID"])
	// NEW_CODE: Assert that the collection name was injected as an environment variable.
	assert.Equal(t, "users-collection", spec.EnvironmentVars["USERS_COLLECTION_NAME"])
}

// TestHydrateTestArchitecture validates the testing hydration path with a runID.
func TestHydrateTestArchitecture(t *testing.T) {
	// ARRANGE
	runID := "xyz123"
	originalServiceName := "my-service"
	originalTopicName := "events-topic"
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: "test-project", Region: "us-central1"},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "service-manager", ServiceAccount: "sm-sa", Deployment: &servicemanager.DeploymentSpec{SourcePath: "."},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{"test-flow": {
			Services: map[string]servicemanager.ServiceSpec{
				originalServiceName: {Name: originalServiceName, ServiceAccount: "my-sa", Deployment: &servicemanager.DeploymentSpec{SourcePath: "."}},
			},
			Resources: servicemanager.CloudResourcesSpec{
				Topics: []servicemanager.TopicConfig{{
					CloudResource: servicemanager.CloudResource{Name: originalTopicName},
					ProducerService: &servicemanager.ServiceMapping{Name: originalServiceName, Lookup: servicemanager.Lookup{
						Key:    "EVENTS_TOPIC_ID",
						Method: servicemanager.LookupYAML,
					}},
				}},
			},
		}},
	}

	// ACT
	_, err := servicemanager.HydrateTestArchitecture(arch, "test-repo", runID, zerolog.Nop())
	require.NoError(t, err)

	// ASSERT
	hydratedSvcName := "my-service-xyz123"
	hydratedTopicName := "events-topic-xyz123"
	hydratedSvc, newKeyExists := arch.Dataflows["test-flow"].Services[hydratedSvcName]
	assert.True(t, newKeyExists, "Hydrated service key should exist")

	// Check that names and references were updated
	assert.Equal(t, hydratedSvcName, hydratedSvc.Name)
	assert.Equal(t, hydratedTopicName, arch.Dataflows["test-flow"].Resources.Topics[0].Name)
	assert.Equal(t, hydratedSvcName, arch.Dataflows["test-flow"].Resources.Topics[0].ProducerService.Name, "ProducerService link should be updated")

	// Assert that the service account names were also hydrated
	assert.Equal(t, "sm-sa-xyz123", arch.ServiceManagerSpec.ServiceAccount)
	assert.Equal(t, "my-sa-xyz123", hydratedSvc.ServiceAccount)

	// Check that derived values (image path and env var) use the new hydrated names
	assert.Contains(t, hydratedSvc.Deployment.Image, "/"+hydratedSvcName+":", "Image path should use the new hydrated service name")
	//assert.Equal(t, hydratedTopicName, hydratedSvc.Deployment.EnvironmentVars["EVENTS_TOPIC_ID"], "Lookup var value should be the new hydrated resource name")
	//test the new ability to use a method other than env var
	assert.NotEqual(t, hydratedTopicName, hydratedSvc.Deployment.EnvironmentVars["EVENTS_TOPIC_ID"], "We should no longer see the topic name in the env variables")
}

func TestHydrateArchitecture_ValidationFailure(t *testing.T) {
	// ARRANGE
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: "test-project", Region: "europe-west1"},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "service-manager",
			// ServiceAccount is missing
		},
	}

	// ACT
	err := servicemanager.HydrateArchitecture(arch, "repo", zerolog.Nop())

	// ASSERT
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ServiceManagerSpec.ServiceAccount is a required field")
}
