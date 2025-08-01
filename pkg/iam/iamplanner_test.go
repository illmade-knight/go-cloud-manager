package iam_test

import (
	"context"
	"testing"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanRolesForApplicationServices(t *testing.T) {
	// ARRANGE: Create a comprehensive test architecture with various IAM requirements.
	arch := &servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{
			ProjectID: "test-project",
			Region:    "europe-west1",
		},
		ServiceManagerSpec: servicemanager.ServiceSpec{
			Name: "service-manager",
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"publisher-service": {
						Name:           "publisher-service",
						ServiceAccount: "publisher-sa",
						Deployment: &servicemanager.DeploymentSpec{
							Region: "europe-west1", // Specific region for this service.
						},
					},
					"subscriber-service": {
						Name:           "subscriber-service",
						ServiceAccount: "subscriber-sa",
					},
					"secret-service": {
						Name:           "secret-service",
						ServiceAccount: "secret-sa",
						Deployment: &servicemanager.DeploymentSpec{
							SecretEnvironmentVars: []servicemanager.SecretEnvVar{
								{Name: "API_KEY", ValueFrom: "my-api-key"},
							},
						},
					},
					"invoker-service": {
						Name:           "invoker-service",
						ServiceAccount: "invoker-sa",
						Dependencies:   []string{"publisher-service"}, // Depends on another service.
					},
					"explicit-policy-service": {
						Name:           "explicit-policy-service",
						ServiceAccount: "explicit-sa",
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "data-topic"},
							ProducerService: "publisher-service", // Implicit publisher role.
						},
					},
					Subscriptions: []servicemanager.SubscriptionConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "data-sub"},
							Topic:           "data-topic",
							ConsumerService: "subscriber-service", // Implicit subscriber role.
						},
					},
					GCSBuckets: []servicemanager.GCSBucket{
						{
							CloudResource: servicemanager.CloudResource{
								Name: "explicit-policy-bucket",
								IAMPolicy: []servicemanager.IAM{ // Explicit policy grant.
									{Name: "explicit-policy-service", Role: "roles/storage.objectViewer"},
								},
							},
						},
					},
				},
			},
		},
	}

	planner := iam.NewRolePlanner(zerolog.Nop())

	// ACT: Run the planner.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()
	_ = ctx // Avoid unused variable warning.

	plan, err := planner.PlanRolesForApplicationServices(arch)
	require.NoError(t, err)
	require.NotNil(t, plan)

	// ASSERT: Verify the generated plan for each service account.
	t.Run("Publisher gets Publisher and Viewer roles", func(t *testing.T) {
		bindings := plan["publisher-sa"]
		require.Len(t, bindings, 2)

		expectedPublisher := iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: "data-topic", Role: "roles/pubsub.publisher"}
		expectedViewer := iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: "data-topic", Role: "roles/pubsub.viewer"}

		assert.Contains(t, bindings, expectedPublisher)
		assert.Contains(t, bindings, expectedViewer)
	})

	t.Run("Subscriber gets Subscriber and Topic Viewer roles", func(t *testing.T) {
		bindings := plan["subscriber-sa"]
		require.Len(t, bindings, 2)

		expectedSubscriber := iam.IAMBinding{ResourceType: "pubsub_subscription", ResourceID: "data-sub", Role: "roles/pubsub.subscriber"}
		expectedViewer := iam.IAMBinding{ResourceType: "pubsub_topic", ResourceID: "data-topic", Role: "roles/pubsub.viewer"}

		assert.Contains(t, bindings, expectedSubscriber)
		assert.Contains(t, bindings, expectedViewer)
	})

	t.Run("Secret user gets Secret Accessor role", func(t *testing.T) {
		bindings := plan["secret-sa"]
		require.Len(t, bindings, 1)

		expectedAccessor := iam.IAMBinding{ResourceType: "secret", ResourceID: "my-api-key", Role: "roles/secretmanager.secretAccessor"}
		assert.Contains(t, bindings, expectedAccessor)
	})

	t.Run("Dependent service gets Invoker role", func(t *testing.T) {
		bindings := plan["invoker-sa"]
		require.Len(t, bindings, 1)

		expectedInvoker := iam.IAMBinding{
			ResourceType:     "cloudrun_service",
			ResourceID:       "publisher-service",
			Role:             "roles/run.invoker",
			ResourceLocation: "europe-west1",
		}
		assert.Contains(t, bindings, expectedInvoker)
	})

	t.Run("Service gets explicit role from iam_access_policy", func(t *testing.T) {
		bindings := plan["explicit-sa"]
		require.Len(t, bindings, 1)

		expectedViewer := iam.IAMBinding{ResourceType: "gcs_bucket", ResourceID: "explicit-policy-bucket", Role: "roles/storage.objectViewer"}
		assert.Contains(t, bindings, expectedViewer)
	})
}
