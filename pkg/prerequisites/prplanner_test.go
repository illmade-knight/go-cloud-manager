package prerequisites_test

import (
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/prerequisites"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrerequisitePlanner_PlanRequiredServices(t *testing.T) {
	// ARRANGE: Create a comprehensive test architecture that uses a wide
	// variety of resource types to trigger all logic paths in the planner.
	arch := &servicemanager.MicroserviceArchitecture{
		ServiceManagerSpec: servicemanager.ServiceManagerSpec{
			ServiceSpec: servicemanager.ServiceSpec{
				Deployment: &servicemanager.DeploymentSpec{
					SecretEnvironmentVars: []servicemanager.SecretEnvVar{
						{Name: "A", ValueFrom: "secret-a"},
					},
				},
			},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"dataflow-1": {
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{{}}, // Triggers pubsub
				},
			},
			"dataflow-2": {
				Resources: servicemanager.CloudResourcesSpec{
					GCSBuckets: []servicemanager.GCSBucket{{}}, // Triggers storage
				},
				Services: map[string]servicemanager.ServiceSpec{
					"bq-service": {
						Deployment: nil, // No deployment, no deployment APIs triggered
					},
				},
			},
			"dataflow-3": {
				Resources: servicemanager.CloudResourcesSpec{
					BigQueryDatasets: []servicemanager.BigQueryDataset{{}}, // Triggers bigquery
				},
				Services: map[string]servicemanager.ServiceSpec{
					"run-service": {
						Deployment: &servicemanager.DeploymentSpec{}, // Triggers deployment-related APIs
					},
				},
			},
		},
	}

	planner := prerequisites.NewPlanner()

	// ACT: Run the planner.
	requiredAPIs := planner.PlanRequiredServices(arch)

	// ASSERT: Verify that the plan contains the correct set of APIs.
	require.NotNil(t, requiredAPIs)

	expectedAPIs := []string{
		"run.googleapis.com",
		"cloudbuild.googleapis.com",
		"artifactregistry.googleapis.com",
		"secretmanager.googleapis.com",
		"iam.googleapis.com",
		"pubsub.googleapis.com",
		"storage.googleapis.com",
		"bigquery.googleapis.com",
	}

	// Use ElementsMatch to assert that the slices contain the same elements,
	// regardless of order, and without duplicates.
	assert.ElementsMatch(t, expectedAPIs, requiredAPIs, "The planned APIs should match the expected set")
	assert.Len(t, requiredAPIs, len(expectedAPIs), "The number of planned APIs should be correct")
}
