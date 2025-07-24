package servicedirector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/microservice"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type OrchestrateRequest struct {
	DataflowName string `json:"dataflow_name"`
}

// NewDirectServiceDirector is a constructor for testing that allows injecting a pre-configured
// ServiceManager and Pub/Sub client (e.g., one connected to an emulator).
func NewDirectServiceDirector(ctx context.Context, cfg *Config, loader servicemanager.ArchitectureIO, sm *servicemanager.ServiceManager, psClient *pubsub.Client, logger zerolog.Logger) (*Director, error) {
	directorLogger := logger.With().Str("component", "Director").Logger()
	arch, err := loader.LoadArchitecture(ctx)
	if err != nil {
		return nil, fmt.Errorf("director: failed to load service definitions: %w", err)
	}
	return newInternalSD(ctx, cfg, arch, sm, psClient, directorLogger)
}

type pubsubCommands struct {
	client              *pubsub.Client
	commandSubscription *pubsub.Subscription
	completionTopic     *pubsub.Topic
}

// Director implements the builder.Service interface for our main application.
type Director struct {
	*microservice.BaseServer
	serviceManager *servicemanager.ServiceManager
	architecture   *servicemanager.MicroserviceArchitecture
	config         *Config // This is now the smaller Config struct
	logger         zerolog.Logger
	commands       *pubsubCommands // we don't require pubsub commands
}

// NewServiceDirector is the primary constructor for production use. It creates
// all necessary internal clients based on the provided configuration.
func NewServiceDirector(ctx context.Context, cfg *Config, loader servicemanager.ArchitectureIO, logger zerolog.Logger) (*Director, error) {
	directorLogger := logger.With().Str("component", "Director").Logger()

	arch, err := loader.LoadArchitecture(ctx)
	if err != nil {
		return nil, fmt.Errorf("director: failed to load service definitions: %w", err)
	}
	directorLogger.Info().Str("projectID", arch.ProjectID).Msg("loaded project architecture")

	sm, err := servicemanager.NewServiceManager(ctx, arch, nil, directorLogger)
	if err != nil {
		return nil, fmt.Errorf("director: failed to create ServiceManager: %w", err)
	}

	// It creates its own Pub/Sub client if needed for the creating the command subscription and listening on it
	var psClient *pubsub.Client
	if arch.ServiceManagerSpec.Deployment != nil {
		psClient, err = pubsub.NewClient(ctx, arch.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("director: failed to create pubsub Client: %w", err)
		}
	}

	return newInternalSD(ctx, cfg, arch, sm, psClient, directorLogger)
}

// newInternalSD is an unexported helper updated to pull configuration from the 'arch' struct.
func newInternalSD(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, sm *servicemanager.ServiceManager, psClient *pubsub.Client, directorLogger zerolog.Logger) (*Director, error) {
	// Get topic and subscription names directly from the architecture spec
	spec := arch.ServiceManagerSpec

	baseServer := microservice.NewBaseServer(directorLogger, cfg.HTTPPort)

	d := &Director{
		BaseServer:     baseServer,
		serviceManager: sm,
		architecture:   arch,
		config:         cfg,
		logger:         directorLogger,
	}

	if spec.Deployment != nil {
		commandTopicID := spec.Deployment.EnvironmentVars["SD_COMMAND_TOPIC"]
		commandSubID := spec.Deployment.EnvironmentVars["SD_COMMAND_SUBSCRIPTION"]
		completionTopicID := spec.Deployment.EnvironmentVars["SD_COMPLETION_TOPIC"]

		// Ensure the subscription for listening to commands exists.
		sub, err := ensureCommandSubscriptionExists(ctx, psClient, commandTopicID, commandSubID)
		if err != nil {
			return nil, err
		}
		topic, err := ensureCompletionTopicExists(ctx, psClient, completionTopicID)
		if err != nil {
			return nil, err
		}
		d.commands = &pubsubCommands{
			client:              psClient,
			commandSubscription: sub,
			completionTopic:     topic,
		}

	}

	mux := baseServer.Mux()
	mux.HandleFunc("/dataflow/verify", d.verifyDataflowHandler)
	mux.HandleFunc("/dataflow/setup", d.setupDataflowHandler)
	mux.HandleFunc("/orchestrate/teardown", d.teardownHandler)

	if d.commands != nil {
		d.listenForCommands(ctx)
		if err := d.publishReadyEvent(ctx); err != nil {
			d.logger.Error().Err(err).Msg("Failed to publish initial 'service_ready' event")
		}
	}

	directorLogger.Info().
		Str("http_port", cfg.HTTPPort).
		Msg("Director initialized and listening for commands.")

	return d, nil
}

// publishReadyEvent is updated to get the topic name from the architecture.
func (d *Director) publishReadyEvent(ctx context.Context) error {
	if d.commands == nil {
		return nil
	}
	d.logger.Info().Msg("Publishing 'service_ready' event...")

	event := orchestration.CompletionEvent{
		Status: orchestration.ServiceDirectorReady,
		Value:  d.architecture.Environment.Name, // Use environment name from arch
	}

	eventData, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal ready event: %w", err)
	}

	result := d.commands.completionTopic.Publish(ctx, &pubsub.Message{Data: eventData})
	_, err = result.Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to publish ready event: %w", err)
	}

	d.logger.Info().Msg("Successfully published 'service_ready' event.")
	return nil
}

// listenForCommands is updated to get the subscription name from the architecture.
func (d *Director) listenForCommands(ctx context.Context) {
	err := d.commands.commandSubscription.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		d.logger.Info().Str("message_id", msg.ID).Msg("Received command message")

		var cmd orchestration.Command
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			d.logger.Error().Err(err).Msg("Failed to unmarshal command message")
			return
		}

		var cmdErr error
		switch cmd.Instruction {
		case orchestration.Setup:
			cmdErr = d.handleSetupCommand(ctx, cmd.Value)
		case orchestration.Teardown:
			cmdErr = d.handleTeardownCommand(ctx, cmd.Value)
		default:
			d.logger.Warn().Str("instruction", string(cmd.Instruction)).Str("value", cmd.Value).Msg("Received unknown command")
		}

		// publish a completion event after processing a command if we have pubsub setup
		err := d.publishCompletionEvent(ctx, cmd.Value, cmdErr)
		if err != nil {
			d.logger.Error().Err(err).Msg("error publishing completion evernt")
		}
	})

	if err != nil && !errors.Is(err, context.Canceled) {
		d.logger.Error().Err(err).Msg("Command listener shut down with error")
	} else {
		d.logger.Info().Msg("Command listener shut down gracefully.")
	}
}

// publishCompletionEvent is updated to get the topic name from the architecture.
func (d *Director) publishCompletionEvent(ctx context.Context, dataflowName string, commandErr error) error {
	d.logger.Info().Str("dataflow", dataflowName).Msg("Publishing completion event...")

	event := orchestration.CompletionEvent{
		Status: orchestration.DataflowComplete,
		Value:  dataflowName,
	}
	if commandErr != nil {
		event.Status = "failure" // A generic failure status
		event.ErrorMessage = commandErr.Error()
	}

	eventData, marshallErr := json.Marshal(event)
	if marshallErr != nil {
		return marshallErr
	}

	result := d.commands.completionTopic.Publish(ctx, &pubsub.Message{Data: eventData})
	msgID, getErr := result.Get(ctx)
	if getErr != nil {
		return getErr
	}
	d.logger.Info().Str("message_id", msgID).Str("status", string(event.Status)).Msg("Completion event published.")
	return nil
}

func (d *Director) handleSetupCommand(ctx context.Context, dataflowName string) error {
	d.logger.Info().Str("dataflow", dataflowName).Msg("Processing 'setup' command...")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if dataflowName == "all" || dataflowName == "" {
		_, err := d.serviceManager.SetupAll(ctx, d.architecture)
		return err
	}
	_, err := d.serviceManager.SetupDataflow(ctx, d.architecture, dataflowName)
	return err
}

func (d *Director) handleTeardownCommand(ctx context.Context, dataflowName string) error {
	d.logger.Info().Str("dataflow", dataflowName).Msg("Processing 'teardown' command...")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if dataflowName == "all" || dataflowName == "" {
		return d.serviceManager.TeardownAll(ctx, d.architecture)
	}
	return d.serviceManager.TeardownDataflow(ctx, d.architecture, dataflowName)
}

func (d *Director) Shutdown() {
	d.BaseServer.Shutdown()

	if d.commands != nil {
		d.commands.client.Close()
	}
}

func (d *Director) GetServiceManager() *servicemanager.ServiceManager {
	return d.serviceManager
}

func (d *Director) setupDataflowHandler(w http.ResponseWriter, r *http.Request) {
	req := OrchestrateRequest{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		d.logger.Warn().Err(err).Msg("Failed to unmarshal orchestrate request")
	}

	if req.DataflowName == "" {
		req.DataflowName = "all"
		return
	}

	if err := d.handleSetupCommand(r.Context(), req.DataflowName); err != nil {
		http.Error(w, fmt.Sprintf("Failed to setup dataflow '%s': %v", req.DataflowName, err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf("Dataflow '%s' setup successfully triggered.", req.DataflowName)))
}

func (d *Director) teardownHandler(w http.ResponseWriter, r *http.Request) {
	if err := d.handleTeardownCommand(r.Context(), "all"); err != nil {
		http.Error(w, fmt.Sprintf("Failed to teardown all dataflows: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("All ephemeral dataflows teardown successfully triggered."))
}

func (d *Director) verifyDataflowHandler(w http.ResponseWriter, r *http.Request) {
	var req VerifyDataflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	d.logger.Info().Str("dataflow", req.DataflowName).Str("service", req.ServiceName).Msg("Received request to verify dataflow.")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	err := d.serviceManager.VerifyDataflow(ctx, d.architecture, req.DataflowName)
	if err != nil {
		d.logger.Error().Err(err).Str("dataflow", req.DataflowName).Msg("Dataflow verification failed")
		http.Error(w, fmt.Sprintf("Dataflow '%s' verification failed: %v", req.DataflowName, err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf("Dataflow '%s' verified successfully.", req.DataflowName)))
}

func ensureCommandSubscriptionExists(ctx context.Context, psClient *pubsub.Client, topicID, subID string) (*pubsub.Subscription, error) {
	topic := psClient.Topic(topicID)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not check for command topic '%s': %w", topicID, err)
	}
	if !exists {
		return nil, fmt.Errorf("command topic '%s' does not exist; it must be created by the orchestrator", topicID)
	}
	sub := psClient.Subscription(subID)
	exists, err = sub.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check for subscription %s: %w", subID, err)
	}
	if !exists {
		log.Info().Str("subscription", subID).Msg("Command subscription not found, creating it now.")
		_, err = psClient.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
			Topic:       topic,
			AckDeadline: 20 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create command subscription %s: %w", subID, err)
		}
	}
	return sub, nil
}

func ensureCompletionTopicExists(ctx context.Context, psClient *pubsub.Client, topicID string) (*pubsub.Topic, error) {
	topic := psClient.Topic(topicID)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not check for command topic '%s': %w", topicID, err)
	}
	if !exists {
		topic, err = psClient.CreateTopic(ctx, topicID)
		if err != nil {
		}
	}

	return topic, nil
}
