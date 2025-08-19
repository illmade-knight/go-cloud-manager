package servicedirector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-dataflow/pkg/microservice"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type OrchestrateRequest struct {
	DataflowName string `json:"dataflow_name"`
}

type pubsubCommands struct {
	client              *pubsub.Client
	commandSubscription *pubsub.Subscription
	completionTopic     *pubsub.Topic
}

// CompletionEvent now includes a field to report back the
// exact set of IAM policies that were applied for verification purposes.
type CompletionEvent struct {
	Status       orchestration.CompletionStatus `json:"status"`
	Value        string                         `json:"value"`
	ErrorMessage string                         `json:"error_message,omitempty"`
	AppliedIAM   map[string]iam.PolicyBinding   `json:"applied_iam,omitempty"`
}

// Director implements the microservice.Service interface for the main application.
type Director struct {
	*microservice.BaseServer
	archMutex      sync.RWMutex
	serviceManager *servicemanager.ServiceManager
	architecture   *servicemanager.MicroserviceArchitecture
	logger         zerolog.Logger
	commands       *pubsubCommands
	iamManager     iam.IAMManager
	iamClient      iam.IAMClient
	ready          chan struct{}
}

// Ready returns a channel that is closed when the service is fully initialized and listening.
func (d *Director) Ready() <-chan struct{} {
	return d.ready
}

// DirectorOption defines the type for the Functional Options Pattern, used for clean construction.
type DirectorOption func(ctx context.Context, d *Director) error

// withServiceManager is an option to configure the Director with a ServiceManager.
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

// withPubSubCommands is an option to configure the Director with its Pub/Sub command infrastructure.
func withPubSubCommands(cfg *Config, psClient *pubsub.Client) DirectorOption {
	return func(ctx context.Context, d *Director) error {
		if cfg.Commands == nil {
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

// publishReadyEvent sends the initial "I'm alive and listening" message.
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

// NewServiceDirector is the primary constructor for production use.
func NewServiceDirector(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*Director, error) {
	directorLogger := logger.With().Str("component", "Director").Logger()
	opts := []DirectorOption{
		withServiceManager(nil),
		withIAM(nil, nil),
	}
	if cfg.Commands != nil {
		opts = append(opts, withPubSubCommands(cfg, nil))
	}
	return newInternalSD(ctx, cfg, arch, directorLogger, opts...)
}

// NewDirectServiceDirector is the constructor for testing, allowing dependency injection.
func NewDirectServiceDirector(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, sm *servicemanager.ServiceManager, iamManager iam.IAMManager, iamClient iam.IAMClient, psClient *pubsub.Client, logger zerolog.Logger) (*Director, error) {
	directorLogger := logger.With().Str("component", "Director").Logger()
	opts := []DirectorOption{
		withServiceManager(sm),
		withIAM(iamManager, iamClient),
		withPubSubCommands(cfg, psClient),
	}
	return newInternalSD(ctx, cfg, arch, directorLogger, opts...)
}

// newInternalSD is the private constructor that applies the functional options.
func newInternalSD(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, directorLogger zerolog.Logger, options ...DirectorOption) (*Director, error) {
	baseServer := microservice.NewBaseServer(directorLogger, cfg.HTTPPort)
	d := &Director{
		BaseServer:   baseServer,
		architecture: arch,
		logger:       directorLogger,
		ready:        make(chan struct{}),
	}
	d.BaseServer.SetReadyChannel(d.ready)

	for _, option := range options {
		if err := option(ctx, d); err != nil {
			return nil, err
		}
	}

	mux := baseServer.Mux()
	mux.HandleFunc(VerifyPath, d.verifyDataflowHandler)
	mux.HandleFunc(SetupPath, d.setupDataflowHandler)
	mux.HandleFunc(TeardownPath, d.teardownHandler)

	directorLogger.Info().Str("http_port", cfg.HTTPPort).Msg("Director initialized.")
	return d, nil
}

// setupAndApplyIAMForDataflow contains the logic for a single dataflow.
func (d *Director) setupAndApplyIAMForDataflow(ctx context.Context, dfName string) error {
	d.logger.Info().Str("dataflow", dfName).Msg("Setting up resources...")
	_, err := d.serviceManager.SetupFoundationalDataflow(ctx, d.architecture, dfName)
	if err != nil {
		return fmt.Errorf("resource setup failed for dataflow '%s': %w", dfName, err)
	}
	d.logger.Info().Str("dataflow", dfName).Msg("Resource setup complete.")

	d.logger.Info().Str("dataflow", dfName).Msg("Applying IAM policies...")
	_, err = d.iamManager.ApplyIAMForDataflow(ctx, d.architecture, dfName)
	if err != nil {
		return fmt.Errorf("iam application failed for dataflow '%s': %w", dfName, err)
	}
	d.logger.Info().Str("dataflow", dfName).Msg("IAM policy application complete.")
	return nil
}

// listenForCommands starts the blocking Pub/Sub message receiver.
// REFACTOR: This function is now updated to handle the new flexible command structure.
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
		var dataflowName string
		var completionValue string // Used to signal completion for the correct waiter.

		switch cmd.Instruction {
		case orchestration.Setup:
			var payload orchestration.FoundationalSetupPayload
			if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
				cmdErr = fmt.Errorf("failed to unmarshal setup payload: %w", err)
			} else {
				dataflowName = payload.DataflowName
				completionValue = dataflowName
				cmdErr = d.handleSetupCommand(ctx, dataflowName)
			}
		case orchestration.SetupDependent:
			var payload orchestration.DependentSetupPayload
			if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
				cmdErr = fmt.Errorf("failed to unmarshal dependent setup payload: %w", err)
			} else {
				dataflowName = payload.DataflowName
				completionValue = dataflowName + "-dependent" // Unique key for this phase
				cmdErr = d.handleDependentSetupCommand(ctx, payload)
			}
		case orchestration.Teardown:
			var payload orchestration.FoundationalSetupPayload
			if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
				cmdErr = fmt.Errorf("failed to unmarshal teardown payload: %w", err)
			} else {
				dataflowName = payload.DataflowName
				completionValue = dataflowName
				cmdErr = d.handleTeardownCommand(ctx, dataflowName)
			}
		default:
			d.logger.Warn().Str("instruction", string(cmd.Instruction)).Msg("Received unknown command")
		}

		// Publish completion event.
		err := d.publishCompletionEvent(ctx, completionValue, cmdErr, nil)
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

// handleSetupCommand orchestrates the setup of a dataflow's foundational resources and IAM.
func (d *Director) handleSetupCommand(ctx context.Context, dataflowName string) error {
	d.logger.Info().Str("dataflow", dataflowName).Msg("Processing 'setup' command for foundational resources...")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	d.archMutex.RLock()
	defer d.archMutex.RUnlock()

	d.logger.Info().Str("dataflow", dataflowName).Msg("Setting up foundational resources...")
	_, err := d.serviceManager.SetupFoundationalDataflow(ctx, d.architecture, dataflowName)
	if err != nil {
		return fmt.Errorf("resource setup failed for dataflow '%s': %w", dataflowName, err)
	}
	d.logger.Info().Str("dataflow", dataflowName).Msg("Foundational resource setup complete.")

	d.logger.Info().Str("dataflow", dataflowName).Msg("Applying IAM policies...")
	_, err = d.iamManager.ApplyIAMForDataflow(ctx, d.architecture, dataflowName)
	if err != nil {
		return fmt.Errorf("iam application failed for dataflow '%s': %w", dataflowName, err)
	}
	d.logger.Info().Str("dataflow", dataflowName).Msg("IAM policy application complete.")
	return nil
}

// REFACTOR: New handler for the dependent setup command.
// handleDependentSetupCommand hydrates the architecture with service URLs and creates dependent resources.
func (d *Director) handleDependentSetupCommand(ctx context.Context, payload orchestration.DependentSetupPayload) error {
	d.logger.Info().Str("dataflow", payload.DataflowName).Msg("Processing 'dependent-resource-setup' command...")
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// 1. Lock the architecture for modification.
	d.archMutex.Lock()
	defer d.archMutex.Unlock()

	// 2. Hydrate the scheduler jobs in the architecture with the provided URLs.
	dataflow, ok := d.architecture.Dataflows[payload.DataflowName]
	if !ok {
		return fmt.Errorf("dataflow '%s' not found in director's architecture", payload.DataflowName)
	}
	if len(dataflow.Resources.CloudSchedulerJobs) > 0 {
		for i, job := range dataflow.Resources.CloudSchedulerJobs {
			url, ok := payload.ServiceURLs[job.TargetService]
			if !ok {
				return fmt.Errorf("URL for target service '%s' not provided for job '%s'", job.TargetService, job.Name)
			}
			d.logger.Debug().Str("job", job.Name).Str("url", url).Msg("Hydrating scheduler job target")
			// Modify the struct in the slice directly.
			dataflow.Resources.CloudSchedulerJobs[i].TargetService = url
		}
		// Put the modified dataflow back into the map.
		d.architecture.Dataflows[payload.DataflowName] = dataflow
	}

	// 3. Call the ServiceManager, which will now see the hydrated architecture.
	return d.serviceManager.SetupDependentDataflow(ctx, d.architecture, payload.DataflowName)
}

func withIAM(iamManager iam.IAMManager, iamClient iam.IAMClient) DirectorOption {
	return func(ctx context.Context, d *Director) error {
		if iamClient == nil {
			var err error
			d.logger.Info().Msg("ServiceDirector creating standard Google IAM Client.")
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

// publishCompletionEvent sends a message back to the orchestrator, now including
// the set of IAM policies that were applied on a successful run.
func (d *Director) publishCompletionEvent(ctx context.Context, dataflowName string, commandErr error, appliedPolicies map[string]iam.PolicyBinding) error {
	d.logger.Info().Str("dataflow", dataflowName).Msg("Publishing completion event...")

	event := CompletionEvent{
		Status: orchestration.DataflowComplete,
		Value:  dataflowName,
	}
	if commandErr != nil {
		event.Status = "failure"
		event.ErrorMessage = commandErr.Error()
	} else {
		// On success, attach the map of applied policies to the event.
		event.AppliedIAM = appliedPolicies
	}

	eventData, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal completion event: %w", err)
	}

	result := d.commands.completionTopic.Publish(ctx, &pubsub.Message{Data: eventData})
	_, err = result.Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to publish completion event: %w", err)
	}
	d.logger.Info().Str("status", string(event.Status)).Msg("Completion event published.")
	return nil
}

// Shutdown gracefully closes all client connections.
func (d *Director) Shutdown(ctx context.Context) {
	_ = d.BaseServer.Shutdown(ctx)
	if d.iamClient != nil {
		_ = d.iamClient.Close()
	}
	if d.commands != nil {
		_ = d.commands.client.Close()
	}
}

// --- HTTP Handlers and Pub/Sub Helpers ---

func (d *Director) setupDataflowHandler(w http.ResponseWriter, r *http.Request) {
	var req OrchestrateRequest
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
	_, _ = w.Write([]byte("Setup triggered."))
}

func (d *Director) teardownHandler(w http.ResponseWriter, r *http.Request) {
	if err := d.handleTeardownCommand(r.Context(), "all"); err != nil {
		http.Error(w, fmt.Sprintf("Failed to teardown all dataflows: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Teardown triggered."))
}

func (d *Director) verifyDataflowHandler(w http.ResponseWriter, r *http.Request) {
	var req VerifyDataflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	err := d.serviceManager.VerifyDataflow(ctx, d.architecture, req.DataflowName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Dataflow '%s' verification failed: %v", req.DataflowName, err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Dataflow verified."))
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

func ensureCommandSubscriptionExists(ctx context.Context, psClient *pubsub.Client, topicID, subID string) (*pubsub.Subscription, error) {
	topic := psClient.Topic(topicID)
	exists, err := topic.Exists(ctx)
	if err != nil || !exists {
		return nil, fmt.Errorf("command topic '%s' does not exist", topicID)
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
