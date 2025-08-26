// in package orchestration/preflight.go

package orchestration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// PreflightValidator runs local tests against service source code to validate
// generated configuration files before a build or deployment is attempted.
type PreflightValidator struct {
	arch   *servicemanager.MicroserviceArchitecture
	logger zerolog.Logger
}

// NewPreflightValidator creates a new validator.
func NewPreflightValidator(arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) *PreflightValidator {
	return &PreflightValidator{
		arch:   arch,
		logger: logger.With().Str("component", "PreflightValidator").Logger(),
	}
}

// Run executes `go test` in each service's directory. It dynamically determines
// the expected resource names from the architecture and passes them to the test
// process as environment variables.
func (v *PreflightValidator) Run() error {
	v.logger.Info().Msg("Running local preflight validation for each service...")

	allServices := v.getAllServiceSpecs()

	for _, service := range allServices {
		if service.Deployment == nil {
			continue
		}
		serviceDir := filepath.Join(service.Deployment.SourcePath, service.Deployment.BuildableModulePath)
		v.logger.Info().Msgf("Validating service in directory: %s", serviceDir)

		cmd := exec.Command("go", "test", "./...")
		cmd.Dir = serviceDir

		// Dynamically determine and inject the expected resource names for this service.
		testEnvVars := v.GenerateTestEnvForService(service.Name)
		cmd.Env = append(os.Environ(), testEnvVars...)

		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("preflight validation failed for service '%s'. Output:\n%s", service.Name, string(output))
		}
	}

	v.logger.Info().Msg("✅ All services passed local preflight validation.")
	return nil
}

// getAllServiceSpecs is a helper to create a flat list of all services,
// including the ServiceDirector, from the architecture.
func (v *PreflightValidator) getAllServiceSpecs() []servicemanager.ServiceSpec {
	var services []servicemanager.ServiceSpec
	if v.arch.ServiceManagerSpec.Deployment != nil {
		services = append(services, v.arch.ServiceManagerSpec.ServiceSpec)
	}
	for _, df := range v.arch.Dataflows {
		for _, svc := range df.Services {
			services = append(services, svc)
		}
	}
	return services
}

// GenerateTestEnvForService inspects the hydrated architecture to find all resources
// linked to a specific service and generates the environment variables for its pre-flight test.
func (v *PreflightValidator) GenerateTestEnvForService(serviceName string) []string {
	var envVars []string

	for _, df := range v.arch.Dataflows {
		// Topics (Producer)
		for _, topic := range df.Resources.Topics {
			if topic.ProducerService != nil && topic.ProducerService.Name == serviceName {
				envVars = append(envVars, fmt.Sprintf("EXPECTED_TOPIC_NAME=%s", topic.Name))
			}
		}
		// Subscriptions (Consumer)
		for _, sub := range df.Resources.Subscriptions {
			if sub.ConsumerService != nil && sub.ConsumerService.Name == serviceName {
				// REFACTOR: The ServiceDirector is a special case and is handled below.
				// This prevents generating a generic and a specific variable for the same resource.
				if serviceName != v.arch.ServiceManagerSpec.Name {
					envVars = append(envVars, fmt.Sprintf("EXPECTED_SUBSCRIPTION_NAME=%s", sub.Name))
				}
			}
		}
		// GCS Buckets (Producer or Consumer)
		for _, bucket := range df.Resources.GCSBuckets {
			isLinked := false
			for _, p := range bucket.Producers {
				if p.Name == serviceName {
					isLinked = true
					break
				}
			}
			if !isLinked {
				for _, c := range bucket.Consumers {
					if c.Name == serviceName {
						isLinked = true
						break
					}
				}
			}
			if isLinked {
				envVars = append(envVars, fmt.Sprintf("EXPECTED_GCS_BUCKET=%s", bucket.Name))
			}
		}
		// BigQuery Tables (Producer or Consumer)
		for _, table := range df.Resources.BigQueryTables {
			isLinked := false
			for _, p := range table.Producers {
				if p.Name == serviceName {
					isLinked = true
					break
				}
			}
			if !isLinked {
				for _, c := range table.Consumers {
					if c.Name == serviceName {
						isLinked = true
						break
					}
				}
			}
			if isLinked {
				envVars = append(envVars, fmt.Sprintf("EXPECTED_BQ_TABLE=%s", table.Name))
				envVars = append(envVars, fmt.Sprintf("EXPECTED_BQ_DATASET=%s", table.Dataset))
			}
		}
		// Firestore Collections (Producer or Consumer)
		for _, coll := range df.Resources.FirestoreCollections {
			isLinked := false
			for _, p := range coll.Producers {
				if p.Name == serviceName {
					isLinked = true
					break
				}
			}
			if !isLinked {
				for _, c := range coll.Consumers {
					if c.Name == serviceName {
						isLinked = true
						break
					}
				}
			}
			if isLinked {
				envVars = append(envVars, fmt.Sprintf("EXPECTED_FS_COLLECTION=%s", coll.Name))
			}
		}
	}

	// Special handling for the ServiceDirector's unique resources
	if serviceName == v.arch.ServiceManagerSpec.Name {
		spec := v.arch.ServiceManagerSpec
		envVars = append(envVars, fmt.Sprintf("EXPECTED_COMMAND_TOPIC=%s", spec.CommandTopic.Name))
		envVars = append(envVars, fmt.Sprintf("EXPECTED_COMPLETION_TOPIC=%s", spec.CompletionTopic.Name))
		// Find the generated subscription name by searching the architecture
		for _, df := range v.arch.Dataflows {
			found := false
			for _, sub := range df.Resources.Subscriptions {
				if sub.ConsumerService != nil && sub.ConsumerService.Name == serviceName {
					envVars = append(envVars, fmt.Sprintf("EXPECTED_COMMAND_SUBSCRIPTION=%s", sub.Name))
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}

	return envVars
}
