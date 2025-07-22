package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// config holds all the application configuration.
type config struct {
	ProjectID           string
	TopicID             string
	Port                string
	AutoPublishEnabled  bool
	AutoPublishInterval time.Duration
	AutoPublishCount    int
}

// getEnv reads and parses configuration from environment variables.
func getEnv() (config, error) {
	cfg := config{
		// Default values
		Port:                "8080",
		AutoPublishEnabled:  true,
		AutoPublishInterval: 20 * time.Second,
		AutoPublishCount:    10,
	}

	cfg.ProjectID = os.Getenv("PROJECT_ID")
	cfg.TopicID = os.Getenv("TOPIC_ID")
	if cfg.ProjectID == "" || cfg.TopicID == "" {
		return cfg, fmt.Errorf("missing required environment variables (PROJECT_ID, TOPIC_ID)")
	}

	if port := os.Getenv("PORT"); port != "" {
		cfg.Port = port
	}

	if enabledStr := os.Getenv("AUTO_PUBLISH_ENABLED"); enabledStr == "true" || enabledStr == "1" {
		cfg.AutoPublishEnabled = true
	}

	if intervalStr := os.Getenv("AUTO_PUBLISH_INTERVAL"); intervalStr != "" {
		interval, err := time.ParseDuration(intervalStr)
		if err != nil {
			return cfg, fmt.Errorf("invalid AUTO_PUBLISH_INTERVAL: %w", err)
		}
		cfg.AutoPublishInterval = interval
	}

	if countStr := os.Getenv("AUTO_PUBLISH_COUNT"); countStr != "" {
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return cfg, fmt.Errorf("invalid AUTO_PUBLISH_COUNT: %w", err)
		}
		cfg.AutoPublishCount = count
	}

	return cfg, nil
}

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("component", "trace-publisher").Logger()

	cfg, err := getEnv()
	if err != nil {
		logger.Fatal().Err(err).Msg("Configuration error")
	}

	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create pubsub client")
	}
	defer client.Close()

	topic := client.Topic(cfg.TopicID)

	// --- Auto-Publisher (IoT Simulation) ---
	if cfg.AutoPublishEnabled {
		go startAutoPublisher(ctx, logger, topic, cfg.AutoPublishInterval, cfg.AutoPublishCount)
	}

	// --- HTTP Trigger Logic ---
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Only POST method is accepted", http.StatusMethodNotAllowed)
			return
		}
		traceID := r.URL.Query().Get("trace_id")
		if traceID == "" {
			http.Error(w, "Missing 'trace_id' query parameter", http.StatusBadRequest)
			return
		}

		result := topic.Publish(r.Context(), &pubsub.Message{Data: []byte(traceID)})
		msgID, err := result.Get(r.Context())
		if err != nil {
			logger.Error().Err(err).Msg("Failed to publish message from HTTP request")
			http.Error(w, "Failed to publish message", http.StatusInternalServerError)
			return
		}
		logger.Info().Str("trace_id", traceID).Str("message_id", msgID).Msg("Successfully published tracer message via HTTP.")
		fmt.Fprintf(w, "Message sent with trace ID: %s", traceID)
	})

	logger.Info().Str("port", cfg.Port).Msg("Starting HTTP server...")
	if err := http.ListenAndServe(":"+cfg.Port, nil); err != nil {
		logger.Fatal().Err(err).Msg("HTTP server failed")
	}
}

// startAutoPublisher runs in the background, sending a set number of messages at a fixed interval.
func startAutoPublisher(ctx context.Context, logger zerolog.Logger, topic *pubsub.Topic, interval time.Duration, count int) {
	logger.Info().
		Str("interval", interval.String()).
		Int("count", count).
		Msg("🚀 Starting auto-publisher to simulate IoT device...")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for i := 0; i < count; i++ {
		select {
		case <-ticker.C:
			traceID := fmt.Sprintf("iot-trace-%s", uuid.NewString())
			result := topic.Publish(ctx, &pubsub.Message{Data: []byte(traceID)})
			msgID, err := result.Get(ctx)

			logCtx := logger.Info().Str("trace_id", traceID).Int("message_num", i+1)
			if err != nil {
				logCtx.Err(err).Msg("Failed to publish auto-message")
			} else {
				logCtx.Str("message_id", msgID).Msg("Successfully published auto-message.")
			}
		case <-ctx.Done():
			logger.Info().Msg("Context cancelled, stopping auto-publisher.")
			return
		}
	}
	logger.Info().Int("count", count).Msg("✅ Auto-publisher finished sending all messages.")
}
