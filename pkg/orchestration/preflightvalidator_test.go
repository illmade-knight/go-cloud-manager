package orchestration_test

import (
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// TestPreflightValidator_generateTestEnvForService is a black-box test that validates
// the logic for generating pre-flight test environment variables.
func TestPreflightValidator_generateTestEnvForService(t *testing.T) {
	// --- Arrange ---
	// Note: We now use the full package path for types since this is an external test.
	testArch := &servicemanager.MicroserviceArchitecture{
		ServiceManagerSpec: servicemanager.ServiceManagerSpec{
			ServiceSpec:     servicemanager.ServiceSpec{Name: "sd-service"},
			CommandTopic:    servicemanager.ServiceMapping{Name: "sd-command-topic"},
			CompletionTopic: servicemanager.ServiceMapping{Name: "sd-completion-topic"},
		},
		Dataflows: map[string]servicemanager.ResourceGroup{
			"servicedirector-infra": {
				Resources: servicemanager.CloudResourcesSpec{
					Subscriptions: []servicemanager.SubscriptionConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "sd-command-topic-sub"},
							ConsumerService: &servicemanager.ServiceMapping{Name: "sd-service"},
						},
					},
				},
			},
			"test-flow": {
				Services: map[string]servicemanager.ServiceSpec{
					"service-a": {Name: "service-a"},
					"service-b": {Name: "service-b"},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource:   servicemanager.CloudResource{Name: "topic-a"},
							ProducerService: &servicemanager.ServiceMapping{Name: "service-a"},
						},
					},
					GCSBuckets: []servicemanager.GCSBucket{
						{
							CloudResource: servicemanager.CloudResource{Name: "bucket-b"},
							ResourceIO:    servicemanager.ResourceIO{Producers: []servicemanager.ServiceMapping{{Name: "service-b"}}},
						},
					},
				},
			},
		},
	}

	// The constructor is now an exported function from the orchestration package.
	validator := orchestration.NewPreflightValidator(testArch, zerolog.Nop())

	testCases := []struct {
		name         string
		serviceName  string
		expectedEnvs []string
	}{
		{
			name:         "Service with Topic",
			serviceName:  "service-a",
			expectedEnvs: []string{"EXPECTED_TOPIC_NAME=topic-a"},
		},
		{
			name:         "Service with GCS Bucket",
			serviceName:  "service-b",
			expectedEnvs: []string{"EXPECTED_GCS_BUCKET=bucket-b"},
		},
		{
			name:        "ServiceDirector Itself",
			serviceName: "sd-service",
			expectedEnvs: []string{
				"EXPECTED_COMMAND_TOPIC=sd-command-topic",
				"EXPECTED_COMPLETION_TOPIC=sd-completion-topic",
				"EXPECTED_COMMAND_SUBSCRIPTION=sd-command-topic-sub",
			},
		},
	}

	// --- Act & Assert ---
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Act: Call the now-exported method to get the environment variables.
			actualEnvs := validator.GenerateTestEnvForService(tc.serviceName)

			// Assert: Use ElementsMatch because the order of generated env vars is not guaranteed.
			// This confirms that the correct set of variables was generated for the service.
			assert.ElementsMatch(t, tc.expectedEnvs, actualEnvs)
		})
	}
}
