package servicedirector

import (
	"flag"
	"github.com/illmade-knight/go-cloud-manager/microservice"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

const (
	verifyPath   = "/dataflow/verify"
	setupPath    = "/dataflow/setup"
	teardownPath = "/orchestrate/teardown"
)

type PubsubConfig struct {
	CommandTopicID    string
	CommandSubID      string
	CompletionTopicID string
}

// Config now only holds configuration that is NOT defined in the architecture spec.
// This is primarily for runtime operational concerns of the service container itself.
type Config struct {
	microservice.BaseConfig
	Commands *PubsubConfig
}

// NewConfig creates a new, minimal Config instance.
// It no longer loads configuration that is now sourced from MicroserviceArchitecture.
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

	// Override with environment variables if they are set.
	if envVal := os.Getenv("SD_HTTP_PORT"); envVal != "" {
		if !strings.HasPrefix(envVal, ":") {
			envVal = ":" + envVal
		}
		cfg.HTTPPort = envVal
	}

	if commandTopicID := os.Getenv("SD_COMMAND_TOPIC"); commandTopicID != "" {
		cfg.Commands = &PubsubConfig{
			CommandTopicID:    commandTopicID,
			CommandSubID:      os.Getenv("SD_COMMAND_SUBSCRIPTION"),
			CompletionTopicID: os.Getenv("SD_COMPLETION_TOPIC"),
		}
	}

	// Cloud Run's special 'PORT' variable takes highest precedence.
	if port := os.Getenv("PORT"); port != "" {
		newPort := ":" + port
		log.Info().Str("old_http_port", cfg.HTTPPort).Str("new_http_port", newPort).Msg("Overriding Director HTTP port with Cloud Run PORT environment variable.")
		cfg.HTTPPort = newPort
	}

	return cfg, nil
}
