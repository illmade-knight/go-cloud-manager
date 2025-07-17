package servicedirector

import (
	"flag"
	"github.com/illmade-knight/go-cloud-manager/microservice"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

// Config holds all configuration for the Director itself.
type Config struct {
	microservice.BaseConfig

	ServicesDefSourceType string
	ServicesDefPath       string
	Environment           string

	CommandTopic        string
	CommandSubscription string
	// Add this field for the completion event topic
	CompletionTopic string

	Firestore struct {
		CollectionPath string
	}
}

// NewConfig creates a new Config instance, loading values from defaults,
// then overriding with flags, and finally overriding with environment variables.
func NewConfig() (*Config, error) {
	// 1. Start with a struct containing all default values.
	cfg := &Config{
		BaseConfig: microservice.BaseConfig{
			LogLevel:  "info",
			HTTPPort:  ":8080",
			ProjectID: "default-gcp-project",
		},
		ServicesDefSourceType: "yaml",
		ServicesDefPath:       "services.yaml",
		Environment:           "dev",
		CommandTopic:          "director-commands",
		CommandSubscription:   "director-command-sub",
		CompletionTopic:       "director-events", // Default name for the completion topic
	}
	cfg.Firestore.CollectionPath = "service-definitions"

	// 2. Define flags to override defaults.
	flag.StringVar(&cfg.Environment, "environment", cfg.Environment, "Operational environment (e.g., dev, prod)")
	flag.StringVar(&cfg.ProjectID, "project-id", cfg.ProjectID, "GCP Project ID")
	flag.StringVar(&cfg.ServicesDefPath, "services-def-path", cfg.ServicesDefPath, "Path to services definition YAML file")
	flag.StringVar(&cfg.HTTPPort, "http-port", cfg.HTTPPort, "HTTP health check port for Director")
	flag.StringVar(&cfg.CommandTopic, "command-topic", cfg.CommandTopic, "Pub/Sub topic for director commands")
	flag.StringVar(&cfg.CommandSubscription, "command-sub", cfg.CommandSubscription, "Pub/Sub subscription for director commands")
	flag.StringVar(&cfg.CompletionTopic, "completion-topic", cfg.CompletionTopic, "Pub/Sub topic for director completion events") // New flag
	flag.Parse()

	// 3. Override with environment variables if they are set.
	if envVal := os.Getenv("SD_ENVIRONMENT"); envVal != "" {
		cfg.Environment = envVal
	}
	if envVal := os.Getenv("SD_PROJECT_ID"); envVal != "" {
		cfg.ProjectID = envVal
	}
	if envVal := os.Getenv("SD_SERVICES_DEF_PATH"); envVal != "" {
		cfg.ServicesDefPath = envVal
	}
	if envVal := os.Getenv("SD_HTTP_PORT"); envVal != "" {
		if !strings.HasPrefix(envVal, ":") {
			envVal = ":" + envVal
		}
		cfg.HTTPPort = envVal
	}
	if envVal := os.Getenv("SD_COMMAND_TOPIC"); envVal != "" {
		cfg.CommandTopic = envVal
	}
	if envVal := os.Getenv("SD_COMMAND_SUB"); envVal != "" {
		cfg.CommandSubscription = envVal
	}
	if envVal := os.Getenv("SD_COMPLETION_TOPIC"); envVal != "" {
		cfg.CompletionTopic = envVal
	}

	// 4. Finally, Cloud Run's special 'PORT' variable takes highest precedence.
	if port := os.Getenv("PORT"); port != "" {
		newPort := ":" + port
		log.Info().Str("old_http_port", cfg.HTTPPort).Str("new_http_port", newPort).Msg("Overriding Director HTTP port with Cloud Run PORT environment variable.")
		cfg.HTTPPort = newPort
	}

	return cfg, nil
}
