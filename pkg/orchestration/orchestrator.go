package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
)

// OrchestratorState represents the various states the orchestrator can be in.
type OrchestratorState string

const (
	StateInitializing         OrchestratorState = "INITIALIZING"
	StateCommandInfraReady    OrchestratorState = "COMMAND_INFRA_READY"
	StateServiceDirectorReady OrchestratorState = "SERVICE_DIRECTOR_READY"
	StateDataflowReady        OrchestratorState = "DATAFLOW_READY"
	StateError                OrchestratorState = "ERROR"
)

// Command is the message sent from the orchestrator to the ServiceDirector.
type Command struct {
	Instruction CommandInstruction `json:"instruction"`
	Value       string             `json:"value"`
}

// CommandInstruction defines the action the ServiceDirector should take.
type CommandInstruction string

const (
	Setup    CommandInstruction = "dataflow-setup"
	Teardown CommandInstruction = "teardown"
)

// CompletionEvent is the message sent from the ServiceDirector back to the orchestrator.
// It now includes the set of policies that were applied for verification.
type CompletionEvent struct {
	Status       CompletionStatus             `json:"status"`
	Value        string                       `json:"value"`
	ErrorMessage string                       `json:"error_message,omitempty"`
	AppliedIAM   map[string]iam.PolicyBinding `json:"applied_iam,omitempty"`
}

// CompletionStatus is used as a key to signal the completion of specific, long-running tasks.
type CompletionStatus string

const (
	ServiceDirectorReady CompletionStatus = "setup_complete"
	DataflowComplete     CompletionStatus = "dataflow_complete"
)

// eventWaiter is a thread-safe mechanism to wait for specific event payloads.
type eventWaiter struct {
	mu      sync.Mutex
	waiters map[string]chan CompletionEvent // Key is dataflow name
}

func newEventWaiter() *eventWaiter {
	return &eventWaiter{
		waiters: make(map[string]chan CompletionEvent),
	}
}

func (ew *eventWaiter) await(key string) <-chan CompletionEvent {
	ew.mu.Lock()
	defer ew.mu.Unlock()
	ch, exists := ew.waiters[key]
	if !exists {
		ch = make(chan CompletionEvent, 1) // Buffer of 1 is important
		ew.waiters[key] = ch
	}
	return ch
}

func (ew *eventWaiter) signal(event CompletionEvent) {
	ew.mu.Lock()
	defer ew.mu.Unlock()
	key := event.Value
	if ch, exists := ew.waiters[key]; exists {
		ch <- event
	} else {
		ch := make(chan CompletionEvent, 1)
		ch <- event
		ew.waiters[key] = ch
	}
}

// Orchestrator manages the deployment and verification of entire dataflows.
type Orchestrator struct {
	archMutex              sync.RWMutex
	arch                   *servicemanager.MicroserviceArchitecture
	logger                 zerolog.Logger
	deployer               deployment.ContainerDeployer
	psClient               *pubsub.Client
	eventWaiter            *eventWaiter
	stateChan              chan OrchestratorState
	cancelFunc             context.CancelFunc
	commandTopic           *pubsub.Topic
	completionTopic        *pubsub.Topic
	completionSubscription *pubsub.Subscription
}

// NewOrchestrator creates a new, fully initialized Orchestrator.
func NewOrchestrator(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, clientOpts ...option.ClientOption) (*Orchestrator, error) {
	projectID := arch.ProjectID
	region := arch.Region
	sourceBucket := fmt.Sprintf("%s_cloudbuild", projectID)

	psClient, err := pubsub.NewClient(ctx, projectID, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub client: %w", err)
	}
	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, region, sourceBucket, logger, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create deployer: %w", err)
	}

	internalCtx, cancel := context.WithCancel(ctx)
	orch := &Orchestrator{
		arch:        arch,
		logger:      logger.With().Str("component", "Orchestrator").Logger(),
		deployer:    deployer,
		psClient:    psClient,
		eventWaiter: newEventWaiter(),
		stateChan:   make(chan OrchestratorState, 10),
		cancelFunc:  cancel,
	}
	go orch.initialize(internalCtx)
	return orch, nil
}

// NewOrchestratorWithClients creates a new Orchestrator with pre-existing clients for testing.
func NewOrchestratorWithClients(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, psClient *pubsub.Client, deployer deployment.ContainerDeployer, logger zerolog.Logger) (*Orchestrator, error) {
	internalCtx, cancel := context.WithCancel(ctx)
	orch := &Orchestrator{
		arch:        arch,
		logger:      logger,
		psClient:    psClient,
		deployer:    deployer,
		eventWaiter: newEventWaiter(),
		stateChan:   make(chan OrchestratorState, 10),
		cancelFunc:  cancel,
	}
	go orch.initialize(internalCtx)
	return orch, nil
}

// DeployServiceDirector deploys the main ServiceDirector service.
func (o *Orchestrator) DeployServiceDirector(ctx context.Context, saEmails map[string]string) (string, error) {
	o.logger.Info().Msg("Starting ServiceDirector deployment...")
	serviceName := o.arch.ServiceManagerSpec.Name
	saEmail, ok := saEmails[serviceName]
	if !ok {
		return "", fmt.Errorf("service account email not found for ServiceDirector '%s'", serviceName)
	}
	deploymentSpec := o.arch.ServiceManagerSpec.Deployment
	if deploymentSpec == nil {
		return "", errors.New("ServiceManagerSpec.Deployment is not defined")
	}
	return o.deployer.BuildAndDeploy(ctx, serviceName, saEmail, *deploymentSpec)
}

// BuildDataflowServices orchestrates the parallel building of all service containers for a dataflow.
func (o *Orchestrator) BuildDataflowServices(ctx context.Context, dataflowName string) (map[string]string, error) {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Beginning parallel build of all application services...")
	o.archMutex.RLock()
	dataflow, ok := o.arch.Dataflows[dataflowName]
	o.archMutex.RUnlock()
	if !ok {
		return nil, fmt.Errorf("dataflow '%s' not found", dataflowName)
	}
	if len(dataflow.Services) == 0 {
		return nil, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(dataflow.Services))
	results := make(chan struct {
		serviceName string
		imageURI    string
	}, len(dataflow.Services))
	for sName, sSpec := range dataflow.Services {
		if sSpec.Deployment == nil {
			continue
		}
		wg.Add(1)
		go func(serviceName string, serviceSpec servicemanager.ServiceSpec) {
			defer wg.Done()
			imageURI, err := o.deployer.Build(ctx, serviceName, *serviceSpec.Deployment)
			if err != nil {
				errs <- fmt.Errorf("failed to build service '%s': %w", serviceName, err)
				return
			}
			results <- struct {
				serviceName string
				imageURI    string
			}{serviceName, imageURI}
		}(sName, sSpec)
	}
	wg.Wait()
	close(errs)
	close(results)
	var buildErrors []string
	for err := range errs {
		buildErrors = append(buildErrors, err.Error())
	}
	if len(buildErrors) > 0 {
		return nil, fmt.Errorf("encountered %d error(s) during build phase: %s", len(buildErrors), strings.Join(buildErrors, "; "))
	}
	builtImages := make(map[string]string)
	for result := range results {
		builtImages[result.serviceName] = result.imageURI
	}
	return builtImages, nil
}

// DeployDataflowServices deploys services using pre-built images.
func (o *Orchestrator) DeployDataflowServices(ctx context.Context, dataflowName string, saEmails map[string]string, builtImages map[string]string, directorURL string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Beginning parallel deployment of all application services...")
	o.archMutex.RLock()
	dataflow, ok := o.arch.Dataflows[dataflowName]
	o.archMutex.RUnlock()
	if !ok {
		return fmt.Errorf("dataflow '%s' not found", dataflowName)
	}
	if len(dataflow.Services) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(dataflow.Services))
	for sName, sSpec := range dataflow.Services {
		if sSpec.Deployment == nil {
			continue
		}
		saEmail, ok := saEmails[sName]
		if !ok {
			return fmt.Errorf("SA email not found for service '%s'", sName)
		}
		imageURI, ok := builtImages[sName]
		if !ok {
			return fmt.Errorf("pre-built image URI not found for service '%s'", sName)
		}
		wg.Add(1)
		go func(serviceName string, serviceSpec servicemanager.ServiceSpec, saEmail, imageURI string) {
			defer wg.Done()
			localSpec := *serviceSpec.Deployment
			localSpec.Image = imageURI
			if localSpec.EnvironmentVars == nil {
				localSpec.EnvironmentVars = make(map[string]string)
			}
			localSpec.EnvironmentVars["SERVICE_DIRECTOR_URL"] = directorURL
			localSpec.EnvironmentVars["PROJECT_ID"] = o.arch.ProjectID
			_, err := o.deployer.DeployService(ctx, serviceName, saEmail, localSpec)
			if err != nil {
				errs <- fmt.Errorf("failed to deploy service '%s': %w", serviceName, err)
			}
		}(sName, sSpec, saEmail, imageURI)
	}
	wg.Wait()
	close(errs)
	var deploymentErrors []string
	for err := range errs {
		deploymentErrors = append(deploymentErrors, err.Error())
	}
	if len(deploymentErrors) > 0 {
		return fmt.Errorf("encountered %d error(s) during deployment: %s", len(deploymentErrors), strings.Join(deploymentErrors, "; "))
	}
	return nil
}

// TriggerDataflowResourceCreation sends a command to the ServiceDirector.
func (o *Orchestrator) TriggerDataflowResourceCreation(ctx context.Context, dataflowName string) error {
	o.logger.Info().Str("dataflow", dataflowName).Str("topic", o.commandTopic.ID()).Msg("Publishing dataflow setup command...")
	cmdPayload := Command{Instruction: Setup, Value: dataflowName}
	cmdMsg, err := json.Marshal(cmdPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal setup command: %w", err)
	}
	result := o.commandTopic.Publish(ctx, &pubsub.Message{Data: cmdMsg})
	_, err = result.Get(ctx)
	return err
}

// AwaitServiceReady blocks until the orchestrator receives a "setup_complete" event.
func (o *Orchestrator) AwaitServiceReady(ctx context.Context, serviceName string) error {
	o.logger.Info().Str("service", serviceName).Msg("Waiting for ServiceDirector ready event...")
	select {
	case event := <-o.eventWaiter.await(string(ServiceDirectorReady)):
		if event.Status != ServiceDirectorReady {
			return fmt.Errorf("received unexpected status event for service director: %s", event.Status)
		}
		o.logger.Info().Str("service", serviceName).Msg("Service readiness confirmed.")
		return nil
	case <-time.After(3 * time.Minute):
		return fmt.Errorf("timeout waiting for 'service_ready' event from '%s'", serviceName)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AwaitDataflowReady now receives and returns the full completion event from the ServiceDirector.
func (o *Orchestrator) AwaitDataflowReady(ctx context.Context, dataflowName string) (CompletionEvent, error) {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Waiting for dataflow completion event...")
	select {
	case event := <-o.eventWaiter.await(dataflowName):
		if event.Status == "failure" {
			return event, fmt.Errorf("dataflow '%s' setup failed remotely: %s", dataflowName, event.ErrorMessage)
		}
		o.logger.Info().Str("dataflow", dataflowName).Msg("Dataflow setup confirmed.")
		o.stateChan <- StateDataflowReady
		return event, nil
	case <-time.After(5 * time.Minute):
		return CompletionEvent{}, fmt.Errorf("timeout waiting for 'setup_complete' event for dataflow '%s'", dataflowName)
	case <-ctx.Done():
		return CompletionEvent{}, ctx.Err()
	}
}

// StateChan returns a read-only channel for monitoring the orchestrator's state.
func (o *Orchestrator) StateChan() <-chan OrchestratorState {
	return o.stateChan
}

// Teardown cleans up all long-lived resources created by the Orchestrator.
func (o *Orchestrator) Teardown(ctx context.Context) error {
	o.cancelFunc()
	var errs []string
	if o.commandTopic != nil {
		if err := o.commandTopic.Delete(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("failed to delete command topic: %v", err))
		}
	}
	if o.completionSubscription != nil {
		if err := o.completionSubscription.Delete(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("failed to delete completion subscription: %v", err))
		}
	}
	if o.completionTopic != nil {
		if err := o.completionTopic.Delete(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("failed to delete completion topic: %v", err))
		}
	}
	if o.psClient != nil {
		_ = o.psClient.Close()
	}
	if len(errs) > 0 {
		return fmt.Errorf("orchestrator cleanup encountered errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// --- Private Methods ---

func (o *Orchestrator) initialize(ctx context.Context) {
	o.stateChan <- StateInitializing
	if err := o.ensureCommandInfra(ctx); err != nil {
		o.logger.Error().Err(err).Msg("Failed to set up command infrastructure")
		o.stateChan <- StateError
		return
	}
	go o.listenForEvents(ctx)
	o.stateChan <- StateCommandInfraReady
}

func (o *Orchestrator) listenForEvents(ctx context.Context) {
	err := o.completionSubscription.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		var event CompletionEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			o.logger.Error().Err(err).Msg("Failed to unmarshal completion event")
			return
		}
		o.eventWaiter.signal(event)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		o.stateChan <- StateError
	}
}

func (o *Orchestrator) ensureCommandInfra(ctx context.Context) error {
	o.archMutex.RLock()
	commandTopicID := o.arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMMAND_TOPIC"]
	completionTopicID := o.arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMPLETION_TOPIC"]
	o.archMutex.RUnlock()

	commandTopic, err := ensureTopicExists(ctx, o.psClient, commandTopicID)
	if err != nil {
		return err
	}
	o.commandTopic = commandTopic
	completionTopic, err := ensureTopicExists(ctx, o.psClient, completionTopicID)
	if err != nil {
		return err
	}
	o.completionTopic = completionTopic
	subID := fmt.Sprintf("orchestrator-listener-%s", uuid.New().String()[:8])
	sub, err := o.psClient.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic:       completionTopic,
		AckDeadline: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("failed to create completion subscription: %w", err)
	}
	o.completionSubscription = sub
	return nil
}

func ensureTopicExists(ctx context.Context, client *pubsub.Client, topicID string) (*pubsub.Topic, error) {
	topic := client.Topic(topicID)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check for topic '%s': %w", topicID, err)
	}
	if exists {
		return topic, nil
	}
	return client.CreateTopic(ctx, topicID)
}
