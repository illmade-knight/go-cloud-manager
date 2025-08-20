package main

import (
	"context"
	_ "embed" // Required for go:embed
	"fmt"
	"net/http"
	"os"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

//go:embed resources.yaml
var resourcesYAML []byte

func readResourcesYAML() (cfg Config, err error) {

	resources := &servicemanager.CloudResourcesSpec{}
	err = yaml.Unmarshal(resourcesYAML, &resources)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse embedded resources.yaml: %w", err)
	}

	lookupMap := orchestration.ReadResourceMappings(resources)

	vt, ok := lookupMap["verify-topic-id"]
	if ok {
		cfg.VerifyTopicID = vt
	} else {
		return cfg, fmt.Errorf("failed to find commands-topic-id in resources.yaml")
	}
	ts, ok := lookupMap["tracer-subscription-id"]
	if ok {
		cfg.SubscriptionID = ts
	} else {
		return cfg, fmt.Errorf("failed to find commands-topic-id in resources.yaml")
	}
	return cfg, nil
}

// Config defines the minimal, local structs needed to unmarshal the service-specific
// resources.yaml file.
type Config struct {
	ProjectID      string
	Port           string
	SubscriptionID string
	VerifyTopicID  string
}

// loadAndValidateConfig centralizes all configuration loading and validation.
// It is now a testable helper function.
func loadAndValidateConfig() (Config, error) {
	cfg, err := readResourcesYAML()
	if err != nil {
		return cfg, err
	}
	// 1. Load resource links from embedded YAML.

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
	cfg, err := loadAndValidateConfig()
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
