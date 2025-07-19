package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
)

// OrchestratorState defines the possible states of the orchestrator.
type OrchestratorState string

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

type CommandMessaging struct {
	CommandTopic    string
	CompletionTopic string
}

// Orchestrator manages the deployment and verification of entire dataflows.
type Orchestrator struct {
	arch             *servicemanager.MicroserviceArchitecture
	logger           zerolog.Logger
	deployer         *deployment.CloudBuildDeployer
	iamOrchestrator  *IAMOrchestrator // Now uses the dedicated IAM orchestrator
	psClient         *pubsub.Client
	dataflowDeployer *DataflowDeployer

	serviceDirectorCmd CommandMessaging

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

// NewOrchestrator creates a new, fully initialized Orchestrator.
func NewOrchestrator(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*Orchestrator, error) {
	if arch.ProjectID == "" {
		return nil, errors.New("ProjectID cannot be empty in the architecture definition")
	}
	projectID := arch.ProjectID
	region := arch.Region
	sourceBucket := fmt.Sprintf("%s_cloudbuild", projectID)

	psClient, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub client: %w", err)
	}

	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, region, sourceBucket, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create deployer: %w", err)
	}

	iamOrchestrator, err := NewIAMOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create iamClient orchestrator: %w", err)
	}

	dataflowDeployer, err := NewDataflowDeployer(ctx, arch, logger, deployer)
	if err != nil {
		return nil, fmt.Errorf("failed to create dataflow deployer: %w", err)
	}

	orch := &Orchestrator{
		arch:             arch,
		logger:           logger,
		deployer:         deployer,
		iamOrchestrator:  iamOrchestrator,
		psClient:         psClient,
		dataflowDeployer: dataflowDeployer,
		stateChan:        make(chan OrchestratorState, 10),
	}

	go orch.initialize(ctx)

	return orch, nil
}

func NewOrchestratorWithClients(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, psClient *pubsub.Client, logger zerolog.Logger) (*Orchestrator, error) {
	if arch.ProjectID == "" {
		return nil, errors.New("ProjectID cannot be empty in the architecture definition")
	}
	projectID := arch.ProjectID
	region := arch.Region
	sourceBucket := fmt.Sprintf("%s_cloudbuild", projectID)

	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, region, sourceBucket, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create deployer: %w", err)
	}

	iamOrchestrator, err := NewIAMOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create iamClient orchestrator: %w", err)
	}

	dataflowDeployer, err := NewDataflowDeployer(ctx, arch, logger, deployer)
	if err != nil {
		return nil, fmt.Errorf("failed to create dataflow deployer: %w", err)
	}

	orch := &Orchestrator{
		arch:             arch,
		logger:           logger,
		deployer:         deployer,
		iamOrchestrator:  iamOrchestrator,
		dataflowDeployer: dataflowDeployer,
		psClient:         psClient,
		stateChan:        make(chan OrchestratorState, 10),
	}

	go orch.initialize(ctx)

	return orch, nil
}

func (o *Orchestrator) initialize(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			o.stateChan <- StateError
			close(o.stateChan)
		}
	}()

	o.stateChan <- StateInitializing

	if err := o.ensureCommandInfra(ctx); err != nil {
		o.logger.Error().Err(err).Msg("Failed to set up command infrastructure")
		o.stateChan <- StateError
		return
	}

	o.stateChan <- StateCommandInfraReady
}

// ensureCommandInfra creates the topics the orchestrator needs to function.
// we need to communicate with serviceManager once it deploys
func (o *Orchestrator) ensureCommandInfra(ctx context.Context) error {

	commandTopic := o.arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMMAND_TOPIC"]
	completionTopic := o.arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMPLETION_TOPIC"]

	o.serviceDirectorCmd.CommandTopic = commandTopic
	o.serviceDirectorCmd.CompletionTopic = completionTopic

	if err := ensureTopicExists(ctx, o.psClient, commandTopic); err != nil {
		return err
	}
	if err := ensureTopicExists(ctx, o.psClient, completionTopic); err != nil {
		return err
	}
	return nil
}

// DeployServiceDirector handles the bootstrap deployment of the ServiceDirector.
func (o *Orchestrator) DeployServiceDirector(ctx context.Context, deploymentServiceAccount, saEmail string) (string, error) {
	o.stateChan <- StateDeployingServiceDirector
	o.logger.Info().Msg("Starting ServiceDirector deployment...")

	deploymentSpec := o.arch.ServiceManagerSpec.Deployment
	if deploymentSpec == nil {
		return "", errors.New("ServiceManagerSpec.Deployment is not defined in the architecture")
	}
	serviceName := o.arch.ServiceManagerSpec.Name

	serviceURL, err := o.deployer.Deploy(ctx, serviceName, deploymentServiceAccount, saEmail, *deploymentSpec)
	if err != nil {
		o.stateChan <- StateError
		return "", fmt.Errorf("servicedirector deployment failed: %w", err)
	}

	if err := o.awaitRevisionReady(ctx, o.arch.ServiceManagerSpec); err != nil {
		o.stateChan <- StateError
		return "", fmt.Errorf("servicedirector never became healthy: %w", err)
	}

	o.stateChan <- StateServiceDirectorReady
	return serviceURL, nil
}

// DeployDataflowServices now delegates the work to the new DataflowDeployer.
func (o *Orchestrator) DeployDataflowServices(ctx context.Context, dataflowName, directorURL string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Handing off to DataflowDeployer to deploy application services...")
	return o.dataflowDeployer.DeployServices(ctx, dataflowName, directorURL)
}

// TeardownServiceDirector handles the deletion of a deployed ServiceDirector.
func (o *Orchestrator) TeardownServiceDirector(ctx context.Context, serviceName string) error {
	o.logger.Info().Str("service_name", serviceName).Msg("Tearing down ServiceDirector...")
	return o.deployer.Teardown(ctx, serviceName)
}

// TriggerDataflowSetup publishes a command to the ServiceDirector to begin setting up resources.
func (o *Orchestrator) TriggerDataflowSetup(ctx context.Context, dataflowName string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Publishing 'setup' command...")

	topic := o.psClient.Topic(o.serviceDirectorCmd.CommandTopic)
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
		Topic:       o.psClient.Topic(o.serviceDirectorCmd.CompletionTopic),
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

// awaitRevisionReady is a private helper to poll a Cloud Run service until it's healthy.
func (o *Orchestrator) awaitRevisionReady(ctx context.Context, spec servicemanager.ServiceSpec) error {
	o.logger.Info().Str("service", spec.Name).Msg("Verifying service is ready by polling the latest revision...")

	regionalEndpoint := fmt.Sprintf("%s-run.googleapis.com:443", spec.Deployment.Region)
	runService, err := run.NewService(ctx, option.WithEndpoint(regionalEndpoint))
	if err != nil {
		return fmt.Errorf("failed to create regional Run client for health check: %w", err)
	}

	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", o.arch.ProjectID, spec.Deployment.Region, spec.Name)

	timeout := time.After(3 * time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout: service %s never became ready", spec.Name)
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
					o.logger.Info().Str("service", spec.Name).Msg("Service is ready.")
					return nil
				}
			}
		}
	}
}
