package servicedirector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-dataflow/pkg/microservice"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// ... (OrchestrateRequest and pubsubCommands are unchanged) ...
type OrchestrateRequest struct {
	DataflowName string `json:"dataflow_name"`
}
type pubsubCommands struct {
	client              *pubsub.Client
	commandSubscription *pubsub.Subscription
	completionTopic     *pubsub.Topic
}

type Director struct {
	*microservice.BaseServer
	serviceManager *servicemanager.ServiceManager
	architecture   *servicemanager.MicroserviceArchitecture
	logger         zerolog.Logger
	commands       *pubsubCommands
	iamManager     iam.IAMManager
	iamClient      iam.IAMClient
	// REFACTOR: The Director now owns its own readiness channel.
	ready chan struct{}
}

// REFACTOR: The Director now has a public Ready() method.
func (d *Director) Ready() <-chan struct{} {
	return d.ready
}

// newInternalSD is updated to initialize the Director's ready channel
// and inject it into the BaseServer.
func newInternalSD(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, directorLogger zerolog.Logger, options ...DirectorOption) (*Director, error) {
	baseServer := microservice.NewBaseServer(directorLogger, cfg.HTTPPort)

	d := &Director{
		BaseServer:   baseServer,
		architecture: arch,
		logger:       directorLogger,
		// REFACTOR: Initialize the ready channel.
		ready: make(chan struct{}),
	}

	// REFACTOR: Inject the director's ready channel into the base server.
	d.BaseServer.SetReadyChannel(d.ready)

	// Apply all the provided configuration options.
	for _, option := range options {
		if err := option(ctx, d); err != nil {
			return nil, err
		}
	}

	mux := baseServer.Mux()
	mux.HandleFunc(VerifyPath, d.verifyDataflowHandler)
	mux.HandleFunc(SetupPath, d.setupDataflowHandler)
	mux.HandleFunc(TeardownPath, d.teardownHandler)

	directorLogger.Info().
		Str("http_port", cfg.HTTPPort).
		Msg("Director initialized.")

	return d, nil
}

// ... (The rest of the file, including constructors and handlers, is unchanged from the Functional Options version) ...
type DirectorOption func(ctx context.Context, d *Director) error

func withServiceManager(sm *servicemanager.ServiceManager) DirectorOption {
	return func(ctx context.Context, d *Director) error {
		if sm == nil {
			var err error
			sm, err = servicemanager.NewServiceManager(ctx, d.architecture, nil, d.logger)
			if err != nil {
				return fmt.Errorf("director: failed to create ServiceManager: %w", err)
			}
		}
		d.serviceManager = sm
		return nil
	}
}
func withIAM(iamManager iam.IAMManager, iamClient iam.IAMClient) DirectorOption {
	return func(ctx context.Context, d *Director) error {
		if iamClient == nil {
			var err error
			iamClient, err = iam.NewGoogleIAMClient(ctx, d.architecture.ProjectID)
			if err != nil {
				return fmt.Errorf("director: failed to create IAM client: %w", err)
			}
		}
		if iamManager == nil {
			var err error
			iamManager, err = iam.NewIAMManager(iamClient, d.logger)
			if err != nil {
				_ = iamClient.Close()
				return fmt.Errorf("director: failed to create IAM manager: %w", err)
			}
		}
		d.iamManager = iamManager
		d.iamClient = iamClient
		return nil
	}
}
func withPubSubCommands(cfg *Config, psClient *pubsub.Client) DirectorOption {
	return func(ctx context.Context, d *Director) error {
		if cfg.Commands == nil {
			d.logger.Info().Msg("no command infrastructure configured")
			return nil
		}

		var err error
		if psClient == nil {
			psClient, err = pubsub.NewClient(ctx, d.architecture.ProjectID, cfg.Commands.Options...)
			if err != nil {
				return fmt.Errorf("director: failed to create pubsub Client: %w", err)
			}
		}

		sub, err := ensureCommandSubscriptionExists(ctx, psClient, cfg.Commands.CommandTopicID, cfg.Commands.CommandSubID)
		if err != nil {
			return err
		}
		topic, err := ensureCompletionTopicExists(ctx, psClient, cfg.Commands.CompletionTopicID)
		if err != nil {
			return err
		}
		d.commands = &pubsubCommands{
			client:              psClient,
			commandSubscription: sub,
			completionTopic:     topic,
		}

		if err = d.publishReadyEvent(ctx); err != nil {
			d.logger.Error().Err(err).Msg("Failed to publish initial 'service_ready' event")
		}
		go d.listenForCommands(ctx)
		return nil
	}
}
func NewServiceDirector(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*Director, error) {
	directorLogger := logger.With().Str("component", "Director").Logger()
	opts := []DirectorOption{
		withServiceManager(nil),
		withIAM(nil, nil),
		withPubSubCommands(cfg, nil),
	}
	return newInternalSD(ctx, cfg, arch, directorLogger, opts...)
}
func NewDirectServiceDirector(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, sm *servicemanager.ServiceManager, iamManager iam.IAMManager, iamClient iam.IAMClient, psClient *pubsub.Client, logger zerolog.Logger) (*Director, error) {
	directorLogger := logger.With().Str("component", "Director").Logger()
	opts := []DirectorOption{
		withServiceManager(sm),
		withIAM(iamManager, iamClient),
		withPubSubCommands(cfg, psClient),
	}
	return newInternalSD(ctx, cfg, arch, directorLogger, opts...)
}

func (d *Director) publishReadyEvent(ctx context.Context) error {
	d.logger.Info().Msg("Publishing 'service_ready' event...")

	event := orchestration.CompletionEvent{
		Status: orchestration.ServiceDirectorReady,
		Value:  d.architecture.Environment.Name,
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
func (d *Director) listenForCommands(ctx context.Context) {
	d.logger.Info().Msg("Starting to listen for commands...")
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

		err := d.publishCompletionEvent(ctx, cmd.Value, cmdErr)
		if err != nil {
			d.logger.Error().Err(err).Msg("error publishing completion event")
		}
	})

	if err != nil && !errors.Is(err, context.Canceled) {
		d.logger.Error().Err(err).Msg("Command listener shut down with error")
	} else {
		d.logger.Info().Msg("Command listener shut down gracefully.")
	}
}
func (d *Director) setupDataflowHandler(w http.ResponseWriter, r *http.Request) {
	req := OrchestrateRequest{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if !errors.Is(err, errors.New("EOF")) {
			d.logger.Warn().Err(err).Msg("Failed to unmarshal orchestrate request")
		}
	}

	if req.DataflowName == "" {
		req.DataflowName = "all"
	}

	if err := d.handleSetupCommand(r.Context(), req.DataflowName); err != nil {
		http.Error(w, fmt.Sprintf("Failed to setup dataflow '%s': %v", req.DataflowName, err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf("Dataflow '%s' setup successfully triggered.", req.DataflowName)))
}
func (d *Director) publishCompletionEvent(ctx context.Context, dataflowName string, commandErr error) error {
	d.logger.Info().Str("dataflow", dataflowName).Msg("Publishing completion event...")

	event := orchestration.CompletionEvent{
		Status: orchestration.DataflowComplete,
		Value:  dataflowName,
	}
	if commandErr != nil {
		event.Status = "failure"
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
	d.logger.Info().Str("dataflow", dataflowName).Msg("Processing 'setup' command with IAM application...")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	setupAndApplyIAM := func(dfName string) error {
		d.logger.Info().Str("dataflow", dfName).Msg("Setting up resources...")
		_, err := d.serviceManager.SetupDataflow(ctx, d.architecture, dfName)
		if err != nil {
			return fmt.Errorf("resource setup failed for dataflow '%s': %w", dfName, err)
		}
		d.logger.Info().Str("dataflow", dfName).Msg("Resource setup complete.")

		d.logger.Info().Str("dataflow", dfName).Msg("Applying IAM policies...")
		dataflow, ok := d.architecture.Dataflows[dfName]
		if !ok {
			return fmt.Errorf("dataflow '%s' not found in architecture spec during IAM application", dfName)
		}
		for serviceName := range dataflow.Services {
			err = d.iamManager.ApplyIAMForService(ctx, d.architecture, dfName, serviceName)
			if err != nil {
				return fmt.Errorf("iam application failed for service '%s' in dataflow '%s': %w", serviceName, dfName, err)
			}
		}
		d.logger.Info().Str("dataflow", dfName).Msg("IAM policy application complete.")
		return nil
	}

	if dataflowName == "all" || dataflowName == "" {
		for name := range d.architecture.Dataflows {
			if err := setupAndApplyIAM(name); err != nil {
				return err
			}
		}
		return nil
	}

	return setupAndApplyIAM(dataflowName)
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
func (d *Director) Shutdown(ctx context.Context) {
	err := d.BaseServer.Shutdown(ctx)
	if err != nil {
		d.logger.Error().Err(err).Msg("Error shutting down base server")
	}

	if d.iamClient != nil {
		if err := d.iamClient.Close(); err != nil {
			d.logger.Error().Err(err).Msg("Error closing IAM client")
		}
	}

	if d.commands != nil {
		_ = d.commands.client.Close()
	}
}
func (d *Director) GetServiceManager() *servicemanager.ServiceManager {
	return d.serviceManager
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
		return nil, fmt.Errorf("could not check for completion topic '%s': %w", topicID, err)
	}
	if !exists {
		log.Info().Str("topic", topicID).Msg("Completion topic not found, creating it now.")
		topic, err = psClient.CreateTopic(ctx, topicID)

		if err != nil {
			return nil, fmt.Errorf("failed to create completion topic '%s': %w", topicID, err)
		}
	}
	return topic, nil
}
