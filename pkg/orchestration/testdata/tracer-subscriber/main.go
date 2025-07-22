package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"cloud.google.com/go/pubsub"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// main now initializes a single, reusable Pub/Sub client.
func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("component", "trace-subscriber").Logger()

	projectID := os.Getenv("PROJECT_ID")
	subID := os.Getenv("SUBSCRIPTION_ID")
	verifyTopicID := os.Getenv("VERIFY_TOPIC_ID") // New: Topic to republish to

	if projectID == "" || subID == "" || verifyTopicID == "" {
		logger.Fatal().Msg("Missing required environment variables (PROJECT_ID, SUBSCRIPTION_ID, VERIFY_TOPIC_ID)")
	}

	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create pubsub client")
	}
	defer client.Close()

	verifyTopic := client.Topic(verifyTopicID)

	// Start a simple health check server
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "OK") })
		http.ListenAndServe(":"+os.Getenv("PORT"), nil)
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
