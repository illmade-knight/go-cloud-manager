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
	microservice.BaseConfig // Embed BaseConfig for common fields

	ServicesDefSourceType string `mapstructure:"services_def_source_type"`
	ServicesDefPath       string `mapstructure:"services_def_path"`
	Environment           string `mapstructure:"environment"`

	Firestore struct {
		CollectionPath string `mapstructure:"collection_path"`
	} `mapstructure:"firestore"`
}

// NewConfig creates a new Config instance, loading values from defaults,
// then overriding with flags, and finally overriding with environment variables.
func NewConfig() (*Config, error) {
	// 1. Start with a struct containing all default values.
	cfg := &Config{
		BaseConfig: microservice.BaseConfig{
			LogLevel:        "info",
			HTTPPort:        ":8080",
			ProjectID:       "default-gcp-project",
			CredentialsFile: "",
		},
		ServicesDefSourceType: "yaml",
		ServicesDefPath:       "services.yaml",
		Environment:           "dev",
	}
	cfg.Firestore.CollectionPath = "service-definitions"

	// 2. Define flags to override defaults.
	// We use the default values from the struct to populate the flag help text.
	flag.StringVar(&cfg.Environment, "environment", cfg.Environment, "Operational environment (e.g., dev, prod)")
	flag.StringVar(&cfg.ProjectID, "project-id", cfg.ProjectID, "GCP Project ID for Director's own operations")
	flag.StringVar(&cfg.ServicesDefPath, "services-def-path", cfg.ServicesDefPath, "Path to services definition YAML file")
	flag.StringVar(&cfg.HTTPPort, "http-port", cfg.HTTPPort, "HTTP health check port for Director")
	flag.Parse()

	// 3. Override with environment variables if they are set.
	// This creates a clear hierarchy: ENV > flag > default.
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
		// Ensure the port starts with a colon if it doesn't already.
		if !strings.HasPrefix(envVal, ":") {
			envVal = ":" + envVal
		}
		cfg.HTTPPort = envVal
	}

	// 4. Finally, Cloud Run's special 'PORT' variable takes highest precedence.
	if port := os.Getenv("PORT"); port != "" {
		newPort := ":" + port
		log.Info().Str("old_http_port", cfg.HTTPPort).Str("new_http_port", newPort).Msg("Overriding Director HTTP port with Cloud Run PORT environment variable.")
		cfg.HTTPPort = newPort
	}

	return cfg, nil
}
