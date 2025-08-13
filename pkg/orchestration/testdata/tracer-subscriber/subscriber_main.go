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
// resources.yaml file. It has no dependency on the core servicemanager package.
type (
	Config struct {
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

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("component", "trace-subscriber").Logger()

	// --- 1. Load configuration from embedded YAML ---
	var cfg Config
	err := yaml.Unmarshal(resourcesYAML, &cfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to parse embedded resources.yaml")
	}

	// Validate that the config contains exactly what this service needs.
	if len(cfg.Subscriptions) != 1 || len(cfg.Topics) != 1 {
		logger.Fatal().
			Int("subscriptions_found", len(cfg.Subscriptions)).
			Int("topics_found", len(cfg.Topics)).
			Msg("Configuration error: expected exactly 1 subscription and 1 topic in resources.yaml")
	}
	subID := cfg.Subscriptions[0].Name
	verifyTopicID := cfg.Topics[0].Name

	// Get remaining config from standard environment variables.
	projectID := os.Getenv("PROJECT_ID")
	port := os.Getenv("PORT")
	if projectID == "" {
		logger.Fatal().Msg("Missing required environment variable: PROJECT_ID")
	}
	if port == "" {
		port = "8080" // Default port
	}

	// --- 2. Initialize and run the service ---
	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create pubsub client")
	}
	defer func() {
		_ = client.Close()
	}()

	verifyTopic := client.Topic(verifyTopicID)
	exists, err := verifyTopic.Exists(ctx)
	if err != nil {
		logger.Fatal().Err(err).Msg("could not verify topic")
	}
	if !exists {
		logger.Warn().Str("topic", verifyTopic.ID()).Msg("client could not find topic")
	}

	// Start a simple health check server
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "OK") })
		err = http.ListenAndServe(":"+port, nil)
		if err != nil {
			logger.Error().Err(err).Msg("Health check server failed")
			return
		}
	}()

	// Start the subscription receiver
	logger.Info().Str("subscription", subID).Msg("Starting message receiver...")
	err = client.Subscription(subID).Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
		log.Printf("Received tracer message: %s", string(m.Data))

		// Republish the message to the verification topic
		result := verifyTopic.Publish(ctx, &pubsub.Message{
			Data: m.Data,
		})
		id, err := result.Get(ctx)
		if err != nil {
			log.Error().Err(err).Msg("Failed to republish verification message")
			m.Nack() // Nack the message so it can be retried
			return
		}
		log.Info().Str("message_id", id).Str("topic", verifyTopic.ID()).Msg("Successfully republished verification message")
		m.Ack()
	})

	if err != nil {
		logger.Fatal().Err(err).Msg("Subscription receiver failed")
	}
}
