package main

import (
	"context"
	_ "embed" // Required for go:embed
	"fmt"
	"net/http"
	"os"

	"cloud.google.com/go/pubsub"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

//go:embed resources.yaml
var resourcesYAML []byte

// Config defines the minimal, local structs needed to unmarshal the service-specific
// resources.yaml file.
type (
	Config struct {
		ProjectID      string
		Port           string
		SubscriptionID string
		VerifyTopicID  string
	}
	resourceConfig struct {
		Topics        []TopicConfig        `yaml:"topics"`
		Subscriptions []SubscriptionConfig `yaml:"subscriptions"`
	}
	TopicConfig struct {
		Name string `yaml:"name"`
	}
	SubscriptionConfig struct {
		Name string `yaml:"name"`
	}
)

// loadAndValidateConfig centralizes all configuration loading and validation.
// It is now a testable helper function.
func loadAndValidateConfig(yamlBytes []byte) (Config, error) {
	var cfg Config

	// 1. Load resource links from embedded YAML.
	var resources resourceConfig
	err := yaml.Unmarshal(yamlBytes, &resources)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse embedded resources.yaml: %w", err)
	}

	// Validate that the config contains exactly what this service needs.
	if len(resources.Subscriptions) != 1 || len(resources.Topics) != 1 {
		return cfg, fmt.Errorf("configuration error: expected exactly 1 subscription and 1 topic, but found %d and %d",
			len(resources.Subscriptions), len(resources.Topics))
	}
	cfg.SubscriptionID = resources.Subscriptions[0].Name
	cfg.VerifyTopicID = resources.Topics[0].Name

	// 2. Load runtime config from standard environment variables.
	cfg.ProjectID = os.Getenv("PROJECT_ID")
	cfg.Port = os.Getenv("PORT")
	if cfg.ProjectID == "" {
		return cfg, fmt.Errorf("missing required environment variable: PROJECT_ID")
	}
	if cfg.Port == "" {
		cfg.Port = "8080" // Default port
	}

	return cfg, nil
}

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("component", "trace-subscriber").Logger()

	// --- 1. Load configuration ---
	cfg, err := loadAndValidateConfig(resourcesYAML)
	if err != nil {
		logger.Fatal().Err(err).Msg("Configuration failed")
	}

	// --- 2. Initialize and run the service ---
	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create pubsub client")
	}
	defer func() {
		_ = client.Close()
	}()

	verifyTopic := client.Topic(cfg.VerifyTopicID)
	exists, err := verifyTopic.Exists(ctx)
	if err != nil {
		logger.Fatal().Err(err).Msg("could not verify topic")
	}
	if !exists {
		logger.Warn().Str("topic", verifyTopic.ID()).Msg("client could not find topic")
	}

	// Start a simple health check server.
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "OK") })
		err = http.ListenAndServe(":"+cfg.Port, nil)
		if err != nil {
			logger.Error().Err(err).Msg("Health check server failed")
			return
		}
	}()

	// Start the subscription receiver.
	logger.Info().Str("subscription", cfg.SubscriptionID).Msg("Starting message receiver...")
	err = client.Subscription(cfg.SubscriptionID).Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
		log.Printf("Received tracer message: %s", string(m.Data))

		// Republish the message to the verification topic.
		result := verifyTopic.Publish(ctx, &pubsub.Message{
			Data: m.Data,
		})
		id, err := result.Get(ctx)
		if err != nil {
			log.Error().Err(err).Msg("Failed to republish verification message")
			m.Nack() // Nack the message so it can be retried.
			return
		}
		log.Info().Str("message_id", id).Str("topic", verifyTopic.ID()).Msg("Successfully republished verification message")
		m.Ack()
	})

	if err != nil {
		logger.Fatal().Err(err).Msg("Subscription receiver failed")
	}
}
