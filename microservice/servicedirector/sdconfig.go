package servicedirector

import (
	"flag"
	"fmt"
	"os"

	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-dataflow/pkg/microservice"
	"github.com/rs/zerolog/log"
	"google.golang.org/api/option"
	"gopkg.in/yaml.v3"
)

// PubsubConfig holds the runtime configuration for Pub/Sub, discovered from the embedded YAML.
type PubsubConfig struct {
	CommandTopicID    string
	CommandSubID      string
	CompletionTopicID string
	Options           []option.ClientOption
}

// Config now only holds configuration that is NOT defined in the architecture spec.
// This is primarily for runtime operational concerns of the service container itself.
type Config struct {
	microservice.BaseConfig
	Commands *PubsubConfig
}

// NewConfig creates a new, minimal Config instance. It only handles runtime
// operational flags and environment variables like the HTTP port.
func NewConfig() (*Config, error) {
	cfg := &Config{
		BaseConfig: microservice.BaseConfig{
			LogLevel: "info",
			HTTPPort: ":8080",
		},
	}

	// Define flags for runtime-only configuration.
	flag.StringVar(&cfg.HTTPPort, "http-port", cfg.HTTPPort, "HTTP health check port for Director")
	flag.Parse()

	cfg.ProjectID = os.Getenv("PROJECT_ID")

	// Cloud Run's special 'PORT' variable takes highest precedence.
	if port := os.Getenv("PORT"); port != "" {
		newPort := ":" + port
		log.Info().Str("old_http_port", cfg.HTTPPort).Str("new_http_port", newPort).Msg("Overriding Director HTTP port with Cloud Run PORT environment variable.")
		cfg.HTTPPort = newPort
	}

	return cfg, nil
}

// ReadResourcesYAML we've moved to using a yaml embed with our main files - this helper reads the yaml pubsub struct into
// the main config
func ReadResourcesYAML(yamlBytes []byte) (*PubsubConfig, error) {
	spec := &servicemanager.CloudResourcesSpec{}
	err := yaml.Unmarshal(yamlBytes, spec)
	if err != nil {
		// Changed from Fatalf to return an error for better handling
		return nil, fmt.Errorf("failed to parse embedded resources.yaml: %w", err)
	}

	lookupMap := orchestration.ReadResourceMappings(spec)

	cfg := &PubsubConfig{}
	var ok bool

	// Get subscription name from the map
	cfg.CommandSubID, ok = lookupMap["command-subscription-id"]
	if !ok {
		return nil, fmt.Errorf("failed to find command-subscription-id in resources.yaml")
	}

	// Find the subscription in the spec to get its topic
	var commandTopicName string
	for _, sub := range spec.Subscriptions {
		if sub.Name == cfg.CommandSubID {
			commandTopicName = sub.Topic
			break
		}
	}
	if commandTopicName == "" {
		return nil, fmt.Errorf("could not find topic for subscription %s", cfg.CommandSubID)
	}
	cfg.CommandTopicID = commandTopicName

	// Get completion topic name from the map
	cfg.CompletionTopicID, ok = lookupMap["completion-topic-id"]
	if !ok {
		return nil, fmt.Errorf("failed to find completion-topic-id in resources.yaml")
	}

	return cfg, nil
}
