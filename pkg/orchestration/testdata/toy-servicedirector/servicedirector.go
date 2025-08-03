package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/rs/zerolog"
)

type Command struct {
	Instruction CommandInstruction `json:"instruction"`
	Value       string             `json:"value"`
}

type CommandInstruction string

const (
	DataflowSetup CommandInstruction = "dataflow-setup"
)

type CompletionEvent struct {
	Status       CompletionStatus `json:"status"`
	Value        string           `json:"value"`
	ErrorMessage string           `json:"error_message,omitempty"`
}

type CompletionStatus string

const (
	ServiceDirectorReady CompletionStatus = "setup_complete"
	DataflowComplete     CompletionStatus = "dataflow_complete"
)

// --- Configuration ---

// Config holds all configuration for the service, loaded from environment variables.
type Config struct {
	ProjectID         string
	CommandTopicID    string
	CommandSubID      string
	CompletionTopicID string
	TracerTopicID     string
	TracerSubID       string
	Port              string
}

// NewConfig creates configuration from environment variables.
func NewConfig() (*Config, error) {
	cfg := &Config{
		ProjectID:         os.Getenv("PROJECT_ID"),
		CommandTopicID:    os.Getenv("SD_COMMAND_TOPIC"),
		CommandSubID:      os.Getenv("SD_COMMAND_SUBSCRIPTION"),
		CompletionTopicID: os.Getenv("SD_COMPLETION_TOPIC"),
		TracerTopicID:     os.Getenv("TRACER_TOPIC_ID"),
		TracerSubID:       os.Getenv("TRACER_SUB_ID"),
		Port:              os.Getenv("PORT"),
	}

	if cfg.ProjectID == "" || cfg.CommandTopicID == "" || cfg.CommandSubID == "" || cfg.CompletionTopicID == "" || cfg.TracerTopicID == "" || cfg.TracerSubID == "" {
		return nil, errors.New("missing required environment variables")
	}

	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	return cfg, nil
}

// ServiceDirector manages the application's lifecycle and dependencies.
type ServiceDirector struct {
	logger          zerolog.Logger
	psClient        *pubsub.Client
	cfg             *Config
	commandSub      *pubsub.Subscription
	completionTopic *pubsub.Topic
}

// NewServiceDirector initializes all dependencies for the service.
func NewServiceDirector(ctx context.Context, cfg *Config, logger zerolog.Logger) (*ServiceDirector, error) {
	psClient, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create Pub/Sub client: %w", err)
	}

	sd := &ServiceDirector{
		logger:   logger,
		psClient: psClient,
		cfg:      cfg,
	}

	if err := sd.setupMessaging(ctx); err != nil {
		return nil, fmt.Errorf("failed to set up messaging infrastructure: %w", err)
	}

	return sd, nil
}

// setupMessaging ensures the required Pub/Sub topics and subscriptions exist.
func (sd *ServiceDirector) setupMessaging(ctx context.Context) error {
	// Ensure completion topic exists (should be created by orchestrator)
	sd.completionTopic = sd.psClient.Topic(sd.cfg.CompletionTopicID)
	exists, err := sd.completionTopic.Exists(ctx)
	if err != nil || !exists {
		return fmt.Errorf("completion topic %s does not exist or failed to check", sd.cfg.CompletionTopicID)
	}

	// Ensure command subscription exists
	commandTopic := sd.psClient.Topic(sd.cfg.CommandTopicID)
	sd.commandSub = sd.psClient.Subscription(sd.cfg.CommandSubID)
	exists, err = sd.commandSub.Exists(ctx)
	if err != nil {
		sd.logger.Warn().Err(err).Msg("Failed to check for subscription, attempting to create")
	}
	if !exists {
		_, err = sd.psClient.CreateSubscription(ctx, sd.cfg.CommandSubID, pubsub.SubscriptionConfig{
			Topic:       commandTopic,
			AckDeadline: 10 * time.Second,
		})
		if err != nil {
			return fmt.Errorf("failed to create command subscription: %w", err)
		}
		sd.logger.Info().Msg("Command subscription created")
	} else {
		sd.logger.Info().Msg("Command subscription already exists")
	}
	sd.logger.Info().Str("topic", commandTopic.ID()).Str("subscription", sd.cfg.CommandSubID).Msg("listening for command")
	return nil
}

// Run starts all service components and waits for a shutdown signal to perform cleanup.
func (sd *ServiceDirector) Run(ctx context.Context) error {
	// Create a channel to receive shutdown signals.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Create a context that will be canceled when a shutdown signal is received.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start the web server in a goroutine so it doesn't block.
	httpServer := sd.startWebServer()
	go func() {
		sd.logger.Info().Str("port", sd.cfg.Port).Msg("Starting web server...")
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			sd.logger.Error().Err(err).Msg("Web server failed")
			// If server fails, we should initiate shutdown.
			quit <- syscall.SIGTERM
		}
	}()

	// Start the command listener in a goroutine.
	go sd.listenForCommands(ctx)

	// Publish the 'service_ready' event.
	if err := sd.publishReadyEvent(ctx); err != nil {
		// This is a critical failure on startup.
		return fmt.Errorf("could not publish ready event: %w", err)
	}

	// Block until a shutdown signal is received.
	<-quit
	sd.logger.Warn().Msg("Shutdown signal received, starting graceful teardown...")

	// Create a new context for the teardown process with a timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	// Gracefully shut down the web server.
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		sd.logger.Error().Err(err).Msg("Error during web server shutdown.")
	}

	// Perform resource cleanup.
	sd.Teardown(shutdownCtx)

	sd.logger.Info().Msg("Service has been shut down gracefully.")
	return nil
}

// listenForCommands starts the blocking Pub/Sub message receiver.
// It now takes a cancellable context to allow for graceful shutdown.
func (sd *ServiceDirector) listenForCommands(ctx context.Context) {
	sd.logger.Info().Str("subscription", sd.commandSub.ID()).Msg("ServiceDirector listening for commands...")
	err := sd.commandSub.Receive(ctx, sd.handleCommand)
	if err != nil && !errors.Is(err, context.Canceled) {
		sd.logger.Error().Err(err).Msg("Command listener shut down unexpectedly")
	}
}

// ## 2. Create the Teardown method ##
// Teardown is the cleanup function that deletes resources created by the service.
func (sd *ServiceDirector) Teardown(ctx context.Context) {
	sd.logger.Info().Msg("Cleaning up tracer resources...")

	// Delete the subscription first.
	sub := sd.psClient.Subscription(sd.cfg.TracerSubID)
	if err := sub.Delete(ctx); err != nil {
		sd.logger.Error().Err(err).Str("subscription", sd.cfg.TracerSubID).Msg("Failed to delete tracer subscription")
	} else {
		sd.logger.Info().Str("subscription", sd.cfg.TracerSubID).Msg("Tracer subscription deleted")
	}

	// Then delete the topic.
	topic := sd.psClient.Topic(sd.cfg.TracerTopicID)
	if err := topic.Delete(ctx); err != nil {
		sd.logger.Error().Err(err).Str("topic", sd.cfg.TracerTopicID).Msg("Failed to delete tracer topic")
	} else {
		sd.logger.Info().Str("topic", sd.cfg.TracerTopicID).Msg("Tracer topic deleted")
	}
}

// ## 3. Minor change to startWebServer to return the server instance ##
// startWebServer configures the HTTP server but doesn't block.
func (sd *ServiceDirector) startWebServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "OK")
	})
	return &http.Server{
		Addr:    ":" + sd.cfg.Port,
		Handler: mux,
	}
}

// --- (Other methods like handleCommand, publishReadyEvent, etc. remain largely the same) ---
// handleCommand is the callback for processing a single Pub/Sub message.
func (sd *ServiceDirector) handleCommand(ctx context.Context, msg *pubsub.Message) {
	msg.Ack()
	sd.logger.Info().Str("data", string(msg.Data)).Msg("Toy Director received command")

	var cmd Command
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		sd.logger.Error().Err(err).Str("data", string(msg.Data)).Msg("Failed to unmarshal command")
		return
	}

	if cmd.Instruction != DataflowSetup {
		sd.logger.Warn().Str("instruction", string(cmd.Instruction)).Msg("Received unexpected instruction")
		return
	}
	sd.logger.Info().Str("instruction", string(cmd.Instruction)).Str("value", cmd.Value).Msg("Processing instruction")

	// Perform the actual resource setup.
	setupErr := sd.setupTracerResources(ctx, sd.cfg.TracerTopicID, sd.cfg.TracerSubID)

	// Prepare the completion event.
	event := CompletionEvent{Status: DataflowComplete, Value: cmd.Value}
	if setupErr != nil {
		sd.logger.Error().Err(setupErr).Msg("Failed to setup tracer resources")
		event.Status = "failure"
		event.ErrorMessage = setupErr.Error()
	}

	// Publish the result of the operation.
	err := sd.publishCompletionEvent(ctx, event)
	if err != nil {
		sd.logger.Err(err).Msg("could not publish completion event")
	}
}

// publishReadyEvent sends the initial "I'm alive and listening" message.
func (sd *ServiceDirector) publishReadyEvent(ctx context.Context) error {
	sd.logger.Info().Msg("Initialization complete, publishing 'service_ready' event...")
	readyEvent := CompletionEvent{Status: ServiceDirectorReady, Value: "toy-service"}
	return sd.publishCompletionEvent(ctx, readyEvent)
}

// publishCompletionEvent marshals and sends an event to the completion topic.
func (sd *ServiceDirector) publishCompletionEvent(ctx context.Context, event CompletionEvent) error {
	eventData, err := json.Marshal(event)
	if err != nil {
		sd.logger.Error().Err(err).Msg("Failed to marshal completion event")
		return err
	}

	result := sd.completionTopic.Publish(ctx, &pubsub.Message{Data: eventData})
	serverID, err := result.Get(ctx)
	if err != nil {
		sd.logger.Error().Err(err).Str("status", string(event.Status)).Msg("Failed to publish completion event")
		return err
	}

	sd.logger.Info().Str("serverID", serverID).Str("status", string(event.Status)).Msg("Published completion event")
	return nil
}

// setupTracerResources creates the topic and subscription for the tracer apps.
func (sd *ServiceDirector) setupTracerResources(ctx context.Context, topicID, subID string) error {
	sd.logger.Info().Str("topic", topicID).Msg("Creating tracer topic...")
	tracerTopic, err := sd.psClient.CreateTopic(ctx, topicID)
	if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		return fmt.Errorf("failed to create tracer topic: %w", err)
	}
	if err == nil {
		sd.logger.Info().Msg("Tracer topic created.")
	} else {
		sd.logger.Info().Msg("Tracer topic already exists.")
		tracerTopic = sd.psClient.Topic(topicID) // Get a handle to the existing topic
	}

	sd.logger.Info().Str("subscription", subID).Msg("Creating tracer subscription...")
	_, err = sd.psClient.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{Topic: tracerTopic})
	if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		return fmt.Errorf("failed to create tracer subscription: %w", err)
	}

	sd.logger.Info().Msg("Finished setting up tracer resources.")
	return nil
}

// main is the application entrypoint.
func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("component", "servicedirector").Logger()
	ctx := context.Background()

	cfg, err := NewConfig()
	if err != nil {
		logger.Fatal().Err(err).Msg("Configuration error")
	}

	sd, err := NewServiceDirector(ctx, cfg, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize ServiceDirector")
	}

	if err := sd.Run(ctx); err != nil {
		logger.Fatal().Err(err).Msg("Service terminated with an error")
	}
}
