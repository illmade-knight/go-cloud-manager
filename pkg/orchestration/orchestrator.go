package orchestration

import (
	"cloud.google.com/go/pubsub"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"sync"
	"time"
)

// OrchestratorState defines the possible states of the orchestrator.
type OrchestratorState string

// We can now define more granular states for each step.
const (
	StateInitializing             OrchestratorState = "INITIALIZING"
	StateCommandInfraReady        OrchestratorState = "COMMAND_INFRA_READY"
	StateDeployingServiceDirector OrchestratorState = "DEPLOYING_SERVICE_DIRECTOR"
	StateServiceDirectorReady     OrchestratorState = "SERVICE_DIRECTOR_READY"
	StateTriggeringDataflow       OrchestratorState = "TRIGGERING_DATAFLOW_SETUP"
	StateAwaitingCompletion       OrchestratorState = "AWAITING_DATAFLOW_COMPLETION"
	StateDataflowReady            OrchestratorState = "DATAFLOW_READY"
	StateError                    OrchestratorState = "ERROR"
)

// Config holds the necessary configuration for the Orchestrator.
type Config struct {
	ProjectID       string
	Region          string
	SourcePath      string
	SourceBucket    string
	ImageRepo       string
	CommandTopic    string
	CompletionTopic string
}

// Orchestrator manages the deployment and verification of entire dataflows.
type Orchestrator struct {
	cfg              Config
	logger           zerolog.Logger
	deployer         *deployment.CloudBuildDeployer
	iam              deployment.IAMClient
	psClient         *pubsub.Client
	dataflowDeployer *DataflowDeployer // <-- Add the new deployer

	stateChan chan OrchestratorState
	closeOnce sync.Once
}

// StateChan returns a read-only channel for monitoring orchestrator state.
func (o *Orchestrator) StateChan() <-chan OrchestratorState {
	return o.stateChan
}

// Close gracefully shuts down the orchestrator and its clients.
func (o *Orchestrator) Close() {
	o.closeOnce.Do(func() {
		close(o.stateChan)
		if o.psClient != nil {
			o.psClient.Close()
		}
	})
}

// NewOrchestrator creates a new, fully initialized Orchestrator for production use.
func NewOrchestrator(ctx context.Context, cfg Config, logger zerolog.Logger) (*Orchestrator, error) {
	psClient, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub client: %w", err)
	}
	return NewOrchestratorFromClients(ctx, cfg, logger, psClient)
}

// NewOrchestratorFromClients creates a new Orchestrator with pre-configured clients.
// NewOrchestratorFromClients creates a new Orchestrator with pre-configured clients.
func NewOrchestratorFromClients(ctx context.Context, cfg Config, logger zerolog.Logger, psClient *pubsub.Client) (*Orchestrator, error) {
	deployer, err := deployment.NewCloudBuildDeployer(ctx, cfg.ProjectID, cfg.Region, cfg.SourceBucket, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create deployer: %w", err)
	}

	iamClient, err := deployment.NewGoogleIAMClient(ctx, cfg.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create iam client: %w", err)
	}

	// Create the new DataflowDeployer instance.
	dataflowDeployer := NewDataflowDeployer(cfg, logger, deployer, iamClient)

	orch := &Orchestrator{
		cfg:              cfg,
		logger:           logger,
		deployer:         deployer,
		iam:              iamClient,
		psClient:         psClient,
		dataflowDeployer: dataflowDeployer, // <-- Store it
		stateChan:        make(chan OrchestratorState, 10),
	}

	go orch.initialize(ctx)

	return orch, nil
}

// initialize handles the setup of command infrastructure and broadcasts state changes.
func (o *Orchestrator) initialize(ctx context.Context) error {
	o.logger.Info().Msg("Orchestrator initializing command infrastructure...")
	o.stateChan <- StateInitializing

	if err := ensureCommandInfra(ctx, o.psClient, o.cfg.CommandTopic, o.cfg.CompletionTopic); err != nil {
		o.logger.Error().Err(err).Msg("Failed to set up command infrastructure")
		o.stateChan <- StateError
		return err
	}

	o.logger.Info().Msg("Command infrastructure is ready.")
	o.stateChan <- StateCommandInfraReady
	return nil
}

// ... (other functions would be updated similarly to broadcast their state) ...
// ensureCommandInfra creates the topics the orchestrator needs to function.
func ensureCommandInfra(ctx context.Context, psClient *pubsub.Client, commandTopic, completionTopic string) error {
	if err := ensureTopicExists(ctx, psClient, commandTopic); err != nil {
		return err
	}
	if err := ensureTopicExists(ctx, psClient, completionTopic); err != nil {
		return err
	}
	return nil
}

// ensureTopicExists is a private helper to create a Pub/Sub topic if it doesn't already exist.
func ensureTopicExists(ctx context.Context, client *pubsub.Client, topicID string) error {
	topic := client.Topic(topicID)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for topic %s: %w", topicID, err)
	}
	if !exists {
		log.Info().Str("topic", topicID).Msg("Topic not found, creating it now.")
		_, err = client.CreateTopic(ctx, topicID)
		if err != nil {
			return fmt.Errorf("failed to create topic %s: %w", topicID, err)
		}
	}
	return nil
}

// DeployServiceDirector handles the special bootstrap deployment of the ServiceDirector.
// It now accepts a suffix for the service name and returns the full name for teardown.
func (o *Orchestrator) DeployServiceDirector(ctx context.Context, modulePath, nameSuffix string) (string, string, error) {
	o.stateChan <- StateDeployingServiceDirector

	serviceName := "service-director"
	if nameSuffix != "" {
		serviceName = fmt.Sprintf("%s-%s", serviceName, nameSuffix)
	}
	o.logger.Info().Str("service_name", serviceName).Msg("Bootstrapping ServiceDirector deployment...")

	runtimeSA := "service-director-sa"
	if nameSuffix != "" {
		runtimeSA = fmt.Sprintf("%s-%s", runtimeSA, nameSuffix)
	}

	runtimeSAEmail, err := o.iam.EnsureServiceAccountExists(ctx, runtimeSA)
	if err != nil {
		return "", "", fmt.Errorf("failed to ensure servicedirector sa: %w", err)
	}

	imageTag := fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:%s", o.cfg.Region, o.cfg.ProjectID, o.cfg.ImageRepo, serviceName, uuid.New().String()[:8])

	spec := servicemanager.DeploymentSpec{
		SourcePath: o.cfg.SourcePath,
		Image:      imageTag,
		CPU:        "1",
		Memory:     "512Mi",
		//BuildEnvironmentVars: map[string]string{
		//	"GOOGLE_BUILDABLE": modulePath,
		//},
		BuildableModulePath: modulePath,
		EnvironmentVars: map[string]string{
			"PROJECT_ID":          o.cfg.ProjectID,
			"SD_COMMAND_TOPIC":    o.cfg.CommandTopic,
			"SD_COMPLETION_TOPIC": o.cfg.CompletionTopic,
		},
	}

	serviceURL, err := o.deployer.Deploy(ctx, serviceName, "", runtimeSAEmail, spec)
	if err != nil {
		o.stateChan <- StateError
		return "", "", fmt.Errorf("servicedirector deployment failed: %w", err)
	}

	if err := o.awaitRevisionReady(ctx, serviceName); err != nil {
		o.stateChan <- StateError
		return "", "", fmt.Errorf("servicedirector never became healthy: %w", err)
	}

	o.stateChan <- StateServiceDirectorReady
	return serviceName, serviceURL, nil
}

// TeardownServiceDirector handles the deletion of a deployed ServiceDirector.
func (o *Orchestrator) TeardownServiceDirector(ctx context.Context, serviceName string) error {
	o.logger.Info().Str("service_name", serviceName).Msg("Tearing down ServiceDirector...")
	return o.deployer.Teardown(ctx, serviceName)
}

// TriggerDataflowSetup publishes a command to the ServiceDirector to begin setting up resources.
func (o *Orchestrator) TriggerDataflowSetup(ctx context.Context, dataflowName string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Publishing 'setup' command...")

	topic := o.psClient.Topic(o.cfg.CommandTopic)
	// Do not Stop() the client's topic, as the client is shared and long-lived.

	cmdPayload := map[string]string{"command": "setup", "dataflow_name": dataflowName}
	cmdMsg, _ := json.Marshal(cmdPayload)

	result := topic.Publish(ctx, &pubsub.Message{Data: cmdMsg})
	msgID, err := result.Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to publish setup command: %w", err)
	}

	o.logger.Info().Str("message_id", msgID).Msg("'setup' command published successfully.")
	return nil
}

// AwaitDataflowReady listens on a topic for a completion event from the ServiceDirector.
func (o *Orchestrator) AwaitDataflowReady(ctx context.Context, dataflowName string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Waiting for completion event from ServiceDirector...")

	subID := fmt.Sprintf("orchestrator-waiter-%s", uuid.New().String()[:8])
	sub, err := o.psClient.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic:       o.psClient.Topic(o.cfg.CompletionTopic),
		AckDeadline: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("failed to create temporary subscription for completion event: %w", err)
	}
	defer sub.Delete(context.Background())

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var receivedEvent bool
	err = sub.Receive(cctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		o.logger.Info().Str("message_id", msg.ID).Msg("Received completion event message")
		receivedEvent = true
		cancel()
	})

	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("error receiving completion event: %w", err)
	}

	if !receivedEvent {
		return fmt.Errorf("timeout waiting for completion event for dataflow: %s", dataflowName)
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("Dataflow setup confirmed.")
	return nil
}

// DeployDataflowServices now delegates the work to the new DataflowDeployer.
func (o *Orchestrator) DeployDataflowServices(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName, directorURL string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Handing off to DataflowDeployer to deploy application services...")
	return o.dataflowDeployer.DeployServices(ctx, arch, dataflowName, directorURL)
}

// awaitRevisionReady is a private helper to poll a Cloud Run service until it's healthy.
func (o *Orchestrator) awaitRevisionReady(ctx context.Context, serviceName string) error {
	o.logger.Info().Str("service", serviceName).Msg("Verifying service is ready by polling the latest revision...")

	regionalEndpoint := fmt.Sprintf("%s-run.googleapis.com:443", o.cfg.Region)
	runService, err := run.NewService(ctx, option.WithEndpoint(regionalEndpoint))
	if err != nil {
		return fmt.Errorf("failed to create regional Run client for health check: %w", err)
	}

	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", o.cfg.ProjectID, o.cfg.Region, serviceName)

	timeout := time.After(3 * time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout: service %s never became ready", serviceName)
		case <-ticker.C:
			svc, err := runService.Projects.Locations.Services.Get(fullServiceName).Do()
			if err != nil {
				continue
			}
			if svc.LatestCreatedRevision == "" {
				continue
			}

			rev, err := runService.Projects.Locations.Services.Revisions.Get(svc.LatestCreatedRevision).Do()
			if err != nil {
				continue
			}

			for _, cond := range rev.Conditions {
				if cond.Type == "Ready" && cond.State == "CONDITION_SUCCEEDED" {
					o.logger.Info().Str("service", serviceName).Msg("Service is ready.")
					return nil
				}
			}
		}
	}
}
