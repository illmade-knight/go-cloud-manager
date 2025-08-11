package iam_test

import (
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findBindingsForSA is a helper that filters the flat slice of bindings for a specific service account.
func findBindingsForSA(plan []iam.IAMBinding, saName string) []iam.IAMBinding {
	var results []iam.IAMBinding
	for _, binding := range plan {
		if binding.ServiceAccount == saName {
			results = append(results, binding)
		}
	}
	return results
}

func TestPlanRolesForApplicationServices(t *testing.T) {
	// ARRANGE:
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: "test-project", Region: "europe-west1"},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"publisher-service":  {Name: "publisher-service", ServiceAccount: "publisher-sa", Deployment: &servicemanager.DeploymentSpec{Region: "europe-west1"}},
					"subscriber-service": {Name: "subscriber-service", ServiceAccount: "subscriber-sa"},
					"secret-service":     {Name: "secret-service", ServiceAccount: "secret-sa", Deployment: &servicemanager.DeploymentSpec{SecretEnvironmentVars: []servicemanager.SecretEnvVar{{Name: "API_KEY", ValueFrom: "my-api-key"}}}},
					"invoker-service":    {Name: "invoker-service", ServiceAccount: "invoker-sa", Dependencies: []string{"publisher-service"}},
					"gcs-writer-service": {Name: "gcs-writer-service", ServiceAccount: "gcs-writer-sa"},
					"bq-reader-service":  {Name: "bq-reader-service", ServiceAccount: "bq-reader-sa"},
					"fs-writer-service":  {Name: "fs-writer-service", ServiceAccount: "fs-writer-sa"},
					"fs-reader-service":  {Name: "fs-reader-service", ServiceAccount: "fs-reader-sa"},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics:        []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: "data-topic"}, ProducerService: &servicemanager.ServiceMapping{Name: "publisher-service"}}},
					Subscriptions: []servicemanager.SubscriptionConfig{{CloudResource: servicemanager.CloudResource{Name: "data-sub"}, Topic: "data-topic", ConsumerService: &servicemanager.ServiceMapping{Name: "subscriber-service"}}},
					GCSBuckets:    []servicemanager.GCSBucket{{CloudResource: servicemanager.CloudResource{Name: "data-lake-bucket"}, Producers: []servicemanager.ServiceMapping{{Name: "gcs-writer-service"}}}},
					BigQueryTables: []servicemanager.BigQueryTable{{
						CloudResource: servicemanager.CloudResource{Name: "events-table"},
						Dataset:       "analytics-dataset",
						Consumers:     []servicemanager.ServiceMapping{{Name: "bq-reader-service"}},
					}},
					// UPDATE: Add a Firestore DB with a producer and consumer to the test case.
					FirestoreDatabases: []servicemanager.FirestoreDatabase{{
						CloudResource: servicemanager.CloudResource{Name: "user-profiles-db"},
						Producers:     []servicemanager.ServiceMapping{{Name: "fs-writer-service"}},
						Consumers:     []servicemanager.ServiceMapping{{Name: "fs-reader-service"}},
					}},
				},
			},
		},
	}

	planner := iam.NewRolePlanner(zerolog.Nop())

	// ACT
	plan, err := planner.PlanRolesForApplicationServices(arch)
	require.NoError(t, err)
	require.NotNil(t, plan)

	// ASSERT:
	t.Run("Publisher gets Publisher and Viewer roles", func(t *testing.T) {
		bindings := findBindingsForSA(plan, "publisher-sa")
		require.Len(t, bindings, 2)
		expectedPublisher := iam.IAMBinding{ServiceAccount: "publisher-sa", ResourceType: "pubsub_topic", ResourceID: "data-topic", Role: "roles/pubsub.publisher"}
		expectedViewer := iam.IAMBinding{ServiceAccount: "publisher-sa", ResourceType: "pubsub_topic", ResourceID: "data-topic", Role: "roles/pubsub.viewer"}
		assert.Contains(t, bindings, expectedPublisher)
		assert.Contains(t, bindings, expectedViewer)
	})

	t.Run("Subscriber gets Subscriber and Topic Viewer roles", func(t *testing.T) {
		bindings := findBindingsForSA(plan, "subscriber-sa")
		require.Len(t, bindings, 3)
		expectedSubscriber := iam.IAMBinding{ServiceAccount: "subscriber-sa", ResourceType: "pubsub_subscription", ResourceID: "data-sub", Role: "roles/pubsub.subscriber"}
		expectedSubViewer := iam.IAMBinding{ServiceAccount: "subscriber-sa", ResourceType: "pubsub_subscription", ResourceID: "data-sub", Role: "roles/pubsub.viewer"}
		expectedTopicViewer := iam.IAMBinding{ServiceAccount: "subscriber-sa", ResourceType: "pubsub_topic", ResourceID: "data-topic", Role: "roles/pubsub.viewer"}
		assert.Contains(t, bindings, expectedSubscriber)
		assert.Contains(t, bindings, expectedSubViewer)
		assert.Contains(t, bindings, expectedTopicViewer)
	})

	t.Run("Secret user gets Secret Accessor role", func(t *testing.T) {
		bindings := findBindingsForSA(plan, "secret-sa")
		require.Len(t, bindings, 1)
		expectedAccessor := iam.IAMBinding{ServiceAccount: "secret-sa", ResourceType: "secret", ResourceID: "my-api-key", Role: "roles/secretmanager.secretAccessor"}
		assert.Contains(t, bindings, expectedAccessor)
	})

	t.Run("Dependent service gets Invoker role", func(t *testing.T) {
		bindings := findBindingsForSA(plan, "invoker-sa")
		require.Len(t, bindings, 1)
		expectedInvoker := iam.IAMBinding{ServiceAccount: "invoker-sa", ResourceType: "cloudrun_service", ResourceID: "publisher-service", Role: "roles/run.invoker", ResourceLocation: "europe-west1"}
		assert.Contains(t, bindings, expectedInvoker)
	})

	t.Run("GCS writer service gets ObjectAdmin role from link", func(t *testing.T) {
		bindings := findBindingsForSA(plan, "gcs-writer-sa")
		require.Len(t, bindings, 1)
		expectedWriter := iam.IAMBinding{ServiceAccount: "gcs-writer-sa", ResourceType: "gcs_bucket", ResourceID: "data-lake-bucket", Role: "roles/storage.objectAdmin"}
		assert.Contains(t, bindings, expectedWriter)
	})

	t.Run("BigQuery consumer gets table-level DataViewer role", func(t *testing.T) {
		bindings := findBindingsForSA(plan, "bq-reader-sa")
		require.Len(t, bindings, 1)
		expectedReader := iam.IAMBinding{ServiceAccount: "bq-reader-sa", ResourceType: "bigquery_table", ResourceID: "analytics-dataset:events-table", Role: "roles/bigquery.dataViewer"}
		assert.Contains(t, bindings, expectedReader)
	})

	// UPDATE: New test cases to verify the Firestore role planning logic.
	t.Run("Firestore writer gets Datastore User role", func(t *testing.T) {
		bindings := findBindingsForSA(plan, "fs-writer-sa")
		require.Len(t, bindings, 1)
		expectedWriter := iam.IAMBinding{ServiceAccount: "fs-writer-sa", ResourceType: "project", ResourceID: "fs-writer-service", Role: "roles/datastore.user"}
		assert.Contains(t, bindings, expectedWriter)
	})

	t.Run("Firestore reader gets Datastore Viewer role", func(t *testing.T) {
		bindings := findBindingsForSA(plan, "fs-reader-sa")
		require.Len(t, bindings, 1)
		expectedReader := iam.IAMBinding{ServiceAccount: "fs-reader-sa", ResourceType: "project", ResourceID: "fs-reader-service", Role: "roles/datastore.viewer"}
		assert.Contains(t, bindings, expectedReader)
	})
}
