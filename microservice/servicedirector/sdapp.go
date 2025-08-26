package servicedirector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/illmade-knight/go-dataflow/pkg/microservice"
	"github.com/rs/zerolog"
)

// pubsubCommands holds the initialized Pub/Sub clients for the Director.
// REFACTOR: This now holds v2 Publisher and Subscriber objects.
type pubsubCommands struct {
	client            *pubsub.Client
	commandSubscriber *pubsub.Subscriber
	completionTopic   *pubsub.Publisher
}

// Director implements the microservice.Service interface for the main application.
type Director struct {
	*microservice.BaseServer
	archMutex      sync.RWMutex
	serviceManager servicemanager.IServiceManager
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
func withServiceManager(sm servicemanager.IServiceManager) DirectorOption {
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
// REFACTOR: Updated to use the v2 pubsub.Client, Publisher, and Subscriber.
func withPubSubCommands(cfg *Config, psClient *pubsub.Client) DirectorOption {
	return func(ctx context.Context, d *Director) error {
		if cfg.Commands == nil {
			d.logger.Warn().Msg("No Pub/Sub command configuration found; Director will not listen for commands.")
			return nil
		}
		var err error
		if psClient == nil {
			psClient, err = pubsub.NewClient(ctx, d.architecture.ProjectID, cfg.Commands.Options...)
			if err != nil {
				return fmt.Errorf("director: failed to create pubsub Client: %w", err)
			}
		}
		err = ensureCommandInfrastructureExists(ctx, psClient, cfg.Commands.CommandTopicID, cfg.Commands.CommandSubID, d.logger)
		if err != nil {
			return err
		}
		err = ensureCompletionTopicExists(ctx, psClient, cfg.Commands.CompletionTopicID, d.logger)
		if err != nil {
			return err
		}

		d.commands = &pubsubCommands{
			client:            psClient,
			commandSubscriber: psClient.Subscriber(cfg.Commands.CommandSubID),
			completionTopic:   psClient.Publisher(cfg.Commands.CompletionTopicID),
		}
		if err = d.publishReadyEvent(ctx); err != nil {
			d.logger.Error().Err(err).Msg("Failed to publish initial 'service_ready' event")
		}
		go d.listenForCommands(ctx)
		return nil
	}
}

// withIAM is an option to configure the Director with an IAMManager.
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

// NewServiceDirector is the primary constructor for production use.
func NewServiceDirector(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*Director, error) {
	directorLogger := logger.With().Str("component", "Director").Logger()
	opts := []DirectorOption{
		withServiceManager(nil),
		withIAM(nil, nil),
		withPubSubCommands(cfg, nil),
	}
	return newInternalSD(ctx, cfg, arch, directorLogger, opts...)
}

// NewDirectServiceDirector is the constructor for testing, allowing dependency injection.
func NewDirectServiceDirector(ctx context.Context, cfg *Config, arch *servicemanager.MicroserviceArchitecture, sm servicemanager.IServiceManager, iamManager iam.IAMManager, iamClient iam.IAMClient, psClient *pubsub.Client, logger zerolog.Logger) (*Director, error) {
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

	directorLogger.Info().Str("http_port", cfg.HTTPPort).Msg("Director initialized.")
	return d, nil
}

// listenForCommands starts the blocking Pub/Sub message receiver.
// REFACTOR: Updated to use the v2 Subscriber.
func (d *Director) listenForCommands(ctx context.Context) {
	d.logger.Info().Msg("Starting to listen for commands...")
	err := d.commands.commandSubscriber.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		d.logger.Info().Str("message_id", msg.ID).Msg("Received command message")
		var cmd orchestration.Command
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			d.logger.Error().Err(err).Msg("Failed to unmarshal command message")
			return
		}

		var cmdErr error
		var dataflowName string
		var completionValue string

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
				completionValue = dataflowName + "-dependent"
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

// handleDependentSetupCommand hydrates the architecture with service URLs and creates dependent resources.
func (d *Director) handleDependentSetupCommand(ctx context.Context, payload orchestration.DependentSetupPayload) error {
	d.logger.Info().Str("dataflow", payload.DataflowName).Msg("Processing 'dependent-resource-setup' command...")
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	d.archMutex.Lock()
	defer d.archMutex.Unlock()

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
			dataflow.Resources.CloudSchedulerJobs[i].TargetService = url
		}
		d.architecture.Dataflows[payload.DataflowName] = dataflow
	}

	return d.serviceManager.SetupDependentDataflow(ctx, d.architecture, payload.DataflowName)
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

// REFACTOR: Updated to use the v2 Publisher.
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

// REFACTOR: Updated to use the v2 Publisher.
func (d *Director) publishCompletionEvent(ctx context.Context, value string, commandErr error, appliedPolicies map[string]iam.PolicyBinding) error {
	d.logger.Info().Str("value", value).Msg("Publishing completion event...")

	event := orchestration.CompletionEvent{
		Status: orchestration.DataflowComplete,
		Value:  value,
	}
	if commandErr != nil {
		event.Status = "failure"
		event.ErrorMessage = commandErr.Error()
	} else {
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
		if d.commands.completionTopic != nil {
			d.commands.completionTopic.Stop()
		}
		if d.commands.client != nil {
			_ = d.commands.client.Close()
		}
	}
}

// REFACTOR: Updated to use the v2 admin clients for resource creation.
func ensureCommandInfrastructureExists(ctx context.Context, psClient *pubsub.Client, topicID, subID string, logger zerolog.Logger) error {
	projectID := psClient.Project()
	topicAdmin := psClient.TopicAdminClient
	subAdmin := psClient.SubscriptionAdminClient

	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	subName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)

	_, err := topicAdmin.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: topicName})
	if err != nil {
		return fmt.Errorf("command topic '%s' does not exist: %w", topicID, err)
	}

	_, err = subAdmin.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: subName})
	if err != nil {
		logger.Info().Str("subscription", subID).Msg("Command subscription not found, creating it now.")
		_, createErr := subAdmin.CreateSubscription(ctx, &pubsubpb.Subscription{
			Name:               subName,
			Topic:              topicName,
			AckDeadlineSeconds: 20,
		})
		if createErr != nil {
			return fmt.Errorf("failed to create command subscription %s: %w", subID, createErr)
		}
	}
	return nil
}

// REFACTOR: Updated to use the v2 admin client for resource creation.
func ensureCompletionTopicExists(ctx context.Context, psClient *pubsub.Client, topicID string, logger zerolog.Logger) error {
	topicAdmin := psClient.TopicAdminClient
	topicName := fmt.Sprintf("projects/%s/topics/%s", psClient.Project(), topicID)

	_, err := topicAdmin.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: topicName})
	if err != nil {
		logger.Info().Str("topic", topicID).Msg("Completion topic not found, creating it now.")
		_, createErr := topicAdmin.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
		if createErr != nil {
			return fmt.Errorf("failed to create completion topic '%s': %w", topicID, createErr)
		}
	}
	return nil
}
