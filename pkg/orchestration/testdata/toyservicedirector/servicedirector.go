package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Command represents the structure of a message from the orchestrator.
type Command struct {
	Name         string `json:"command"`
	DataflowName string `json:"dataflow_name"`
}

// CompletionEvent is the message published back to the orchestrator.
type CompletionEvent struct {
	Status       string `json:"status"`
	DataflowName string `json:"dataflow_name"`
	ErrorMessage string `json:"error_message,omitempty"`
}

func main() {
	logger := log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	ctx := context.Background()

	// 1. Get configuration from environment variables.
	projectID := os.Getenv("PROJECT_ID")
	commandTopicID := os.Getenv("SD_COMMAND_TOPIC")
	commandSubID := os.Getenv("SD_COMMAND_SUBSCRIPTION")
	completionTopicID := os.Getenv("SD_COMPLETION_TOPIC")
	tracerTopicID := os.Getenv("TRACER_TOPIC_ID")
	tracerSubID := os.Getenv("TRACER_SUB_ID")

	if projectID == "" || commandTopicID == "" || commandSubID == "" || completionTopicID == "" || tracerTopicID == "" || tracerSubID == "" {
		logger.Fatal().Msg("Toy ServiceDirector is missing required environment variables")
	}

	// 2. Create a Pub/Sub client. Because the test harness will set the
	//    PUBSUB_EMULATOR_HOST, this client will automatically connect to the emulator.
	psClient, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create Pub/Sub client")
	}
	defer psClient.Close()

	// 3. Ensure the command subscription exists.
	commandTopic := psClient.Topic(commandTopicID)
	sub := psClient.Subscription(commandSubID)
	exists, err := sub.Exists(ctx)
	if !exists {
		if err != nil {
			logger.Warn().Err(err).Msg("Failed to check for subscription, attempting to create")
		}
		_, err = psClient.CreateSubscription(ctx, commandSubID, pubsub.SubscriptionConfig{Topic: commandTopic, AckDeadline: 10 * time.Second})
		if err != nil {
			logger.Fatal().Err(err).Msg("Failed to create command subscription")
		}
	}

	// 4. Start a simple web server for Cloud Run health checks.
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "OK") })
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		http.ListenAndServe(":"+port, nil)
	}()

	// 5. Listen for commands, perform the setup, and publish a reply.
	logger.Info().Str("subscription", commandSubID).Msg("Toy ServiceDirector listening for commands...")
	err = sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		logger.Info().Str("data", string(msg.Data)).Msg("Toy Director received command")

		var cmd Command
		json.Unmarshal(msg.Data, &cmd)

		// Perform the actual resource setup using the Pub/Sub client.
		setupErr := setupTracerResources(ctx, psClient, tracerTopicID, tracerSubID)

		// Publish the completion event.
		completionTopic := psClient.Topic(completionTopicID)
		event := CompletionEvent{Status: "success", DataflowName: cmd.DataflowName}
		if setupErr != nil {
			event.Status = "failure"
			event.ErrorMessage = setupErr.Error()
		}
		eventData, _ := json.Marshal(event)

		result := completionTopic.Publish(ctx, &pubsub.Message{Data: eventData})
		result.Get(ctx)
	})

	if err != nil {
		logger.Fatal().Err(err).Msg("Receive error")
	}
}

// setupTracerResources creates the topic and subscription for the tracer apps.
func setupTracerResources(ctx context.Context, psClient *pubsub.Client, topicID, subID string) error {
	log.Info().Str("topic", topicID).Msg("Toy Director creating tracer topic...")
	tracerTopic, err := psClient.CreateTopic(ctx, topicID)
	if err != nil {
		// It might already exist from a previous failed run, so we can ignore that.
		if !strings.Contains(err.Error(), "AlreadyExists") {
			return fmt.Errorf("failed to create tracer topic: %w", err)
		}
		// If it exists, just get a handle to it.
		tracerTopic = psClient.Topic(topicID)
	}

	log.Info().Str("subscription", subID).Msg("Toy Director creating tracer subscription...")
	_, err = psClient.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic: tracerTopic,
	})
	if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		return fmt.Errorf("failed to create tracer subscription: %w", err)
	}

	log.Info().Msg("Toy Director finished setting up tracer resources.")
	return nil
}
