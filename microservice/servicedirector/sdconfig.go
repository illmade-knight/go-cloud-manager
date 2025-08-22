package servicedirector

import (
	"flag"
	"os"

	"github.com/illmade-knight/go-dataflow/pkg/microservice"
	"github.com/rs/zerolog/log"
	"google.golang.org/api/option"
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
