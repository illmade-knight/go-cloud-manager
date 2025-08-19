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

type OrchestratorState string

const (
	StateInitializing         OrchestratorState = "INITIALIZING"
	StateCommandInfraReady    OrchestratorState = "COMMAND_INFRA_READY"
	StateServiceDirectorReady OrchestratorState = "SERVICE_DIRECTOR_READY"
	StateDataflowReady        OrchestratorState = "DATAFLOW_READY"
	StateError                OrchestratorState = "ERROR"
)

type Command struct {
	Instruction CommandInstruction `json:"instruction"`
	Payload     json.RawMessage    `json:"payload"`
}

// FoundationalSetupPayload is the payload for the initial 'dataflow-setup' command.
type FoundationalSetupPayload struct {
	DataflowName string `json:"dataflow_name"`
}
type CommandInstruction string

const (
	Setup    CommandInstruction = "dataflow-setup"
	Teardown CommandInstruction = "teardown"
	// REFACTOR: Added a new, explicit command for dependent resource setup.
	SetupDependent CommandInstruction = "dependent-resource-setup"
)

// DependentSetupPayload contains the dataflow name and the map of deployed service URLs.
type DependentSetupPayload struct {
	DataflowName string            `json:"dataflow_name"`
	ServiceURLs  map[string]string `json:"service_urls"`
}

type CompletionEvent struct {
	Status       CompletionStatus             `json:"status"`
	Value        string                       `json:"value"`
	ErrorMessage string                       `json:"error_message,omitempty"`
	AppliedIAM   map[string]iam.PolicyBinding `json:"applied_iam,omitempty"`
}
type CompletionStatus string

const (
	ServiceDirectorReady CompletionStatus = "setup_complete"
	DataflowComplete     CompletionStatus = "dataflow_complete"
)

const ServiceDirector = "service-director"

// Orchestrator manages the deployment and verification of entire dataflows.
type Orchestrator struct {
	archMutex              sync.RWMutex
	arch                   *servicemanager.MicroserviceArchitecture
	logger                 zerolog.Logger
	deployer               deployment.ContainerDeployer
	psClient               *pubsub.Client
	cancelFunc             context.CancelFunc
	commandTopic           *pubsub.Topic
	completionTopic        *pubsub.Topic
	completionSubscription *pubsub.Subscription
	stateChan              chan OrchestratorState

	// This is the simple, thread-safe map of channels.
	// It will hold one channel for each pending operation (e.g. one for each dataflow setup or teardown completion event).
	waiters      map[string]chan CompletionEvent
	waitersMutex sync.Mutex
}

// NewOrchestrator creates a new, fully initialized Orchestrator.
func NewOrchestrator(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, clientOpts ...option.ClientOption) (*Orchestrator, error) {
	projectID := arch.ProjectID
	region := arch.Region
	sourceBucket := fmt.Sprintf("%s_cloudbuild", projectID)
	psClient, err := pubsub.NewClient(ctx, projectID, clientOpts...)
	if err != nil {
		return nil, err
	}
	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, region, sourceBucket, logger, clientOpts...)
	if err != nil {
		return nil, err
	}

	internalCtx, cancel := context.WithCancel(ctx)
	orch := &Orchestrator{
		arch:       arch,
		logger:     logger.With().Str("component", "Orchestrator").Logger(),
		deployer:   deployer,
		psClient:   psClient,
		cancelFunc: cancel,
		stateChan:  make(chan OrchestratorState, 10),
		waiters:    make(map[string]chan CompletionEvent),
	}

	if err := orch.initialize(internalCtx); err != nil {
		return nil, err
	}
	return orch, nil
}

// NewOrchestratorWithClients creates a new Orchestrator with pre-existing clients for testing.
func NewOrchestratorWithClients(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, psClient *pubsub.Client, deployer deployment.ContainerDeployer, logger zerolog.Logger) (*Orchestrator, error) {
	internalCtx, cancel := context.WithCancel(ctx)
	orch := &Orchestrator{
		arch:       arch,
		logger:     logger,
		psClient:   psClient,
		deployer:   deployer,
		cancelFunc: cancel,
		stateChan:  make(chan OrchestratorState, 10),
		waiters:    make(map[string]chan CompletionEvent),
	}
	if err := orch.initialize(internalCtx); err != nil {
		return nil, err
	}
	return orch, nil
}

// StateChan returns a read-only channel for monitoring the orchestrator's state.
func (o *Orchestrator) StateChan() <-chan OrchestratorState {
	return o.stateChan
}

// getWaiterChannel is the new, simple, thread-safe way to get a channel to wait on.
// It creates the channel just in time, before the wait begins.
func (o *Orchestrator) getWaiterChannel(key string) <-chan CompletionEvent {
	o.waitersMutex.Lock()
	defer o.waitersMutex.Unlock()

	ch, exists := o.waiters[key]
	if !exists {
		ch = make(chan CompletionEvent, 1) // Buffered channel is key to preventing lost signals.
		o.waiters[key] = ch
	}
	return ch
}

// AwaitServiceReady now correctly waits for the "setup_complete" status.
func (o *Orchestrator) AwaitServiceReady(ctx context.Context, serviceName string) error {
	o.logger.Info().Str("service", serviceName).Msg("Waiting for ServiceDirector ready event...")

	// HIGHLIGHT: Step 1 - We get the specific channel we need to listen on *before* we start waiting.
	waitChan := o.getWaiterChannel(serviceName)

	select {
	case event := <-waitChan:
		if event.Status != ServiceDirectorReady {
			return fmt.Errorf("received unexpected status event for service director: %s", event.Status)
		}
		o.logger.Info().Str("service", serviceName).Msg("Service readiness confirmed.")
		o.stateChan <- StateServiceDirectorReady
		return nil
	case <-time.After(6 * time.Minute):
		return fmt.Errorf("timeout waiting for 'service_ready' event from '%s'", serviceName)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AwaitDataflowReady now correctly waits for the "dataflow_complete" status for the specific dataflow.
func (o *Orchestrator) AwaitDataflowReady(ctx context.Context, dataflowName string) (CompletionEvent, error) {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Waiting for dataflow completion event...")

	// HIGHLIGHT: Step 1 - We get the specific channel for this dataflow *before* we start waiting.
	waitChan := o.getWaiterChannel(dataflowName)

	select {
	case event := <-waitChan:
		if event.Status == "failure" {
			return event, fmt.Errorf("dataflow '%s' setup failed remotely: %s", dataflowName, event.ErrorMessage)
		}
		o.logger.Info().Str("dataflow", dataflowName).Msg("Dataflow setup confirmed.")
		o.stateChan <- StateDataflowReady
		return event, nil
	case <-time.After(6 * time.Minute):
		return CompletionEvent{}, fmt.Errorf("timeout waiting for 'setup_complete' event for dataflow '%s'", dataflowName)
	case <-ctx.Done():
		return CompletionEvent{}, ctx.Err()
	}
}

// initialize is a synchronous setup method that reports its state.
func (o *Orchestrator) initialize(ctx context.Context) error {
	o.stateChan <- StateInitializing
	o.logger.Info().Msg("Initializing Orchestrator command infrastructure...")
	if err := o.ensureCommandInfra(ctx); err != nil {
		o.stateChan <- StateError
		return fmt.Errorf("failed to set up command infrastructure: %w", err)
	}
	go o.listenForEvents(ctx)
	o.logger.Info().Msg("Orchestrator initialization complete.")
	o.stateChan <- StateCommandInfraReady
	return nil
}

// listenForEvents now correctly routes signals to the appropriate dedicated channel.
func (o *Orchestrator) listenForEvents(ctx context.Context) {
	o.logger.Info().Msg("Listening for service director events...")
	err := o.completionSubscription.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		var event CompletionEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			o.logger.Error().Err(err).Msg("Failed to unmarshal completion event")
			return
		}
		o.logger.Info().Str("status", string(event.Status)).Str("value", event.Value).Msg("Received event")

		// HIGHLIGHT: Step 2 - When a message comes in, we find the correct channel in the map
		// and send the event to it. The buffered channel prevents this from blocking.
		o.waitersMutex.Lock()
		var key string
		if event.Status == ServiceDirectorReady {
			key = ServiceDirector
		} else {
			// if the event is not ServiceDirectorReady then it is a dataflow completion event
			key = event.Value
		}

		if ch, ok := o.waiters[key]; ok {
			ch <- event
		} else {
			o.logger.Warn().Str("key", key).Msg("Received event for which there was no active waiter.")
		}
		o.waitersMutex.Unlock()
		msg.Ack()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		o.logger.Error().Err(err).Msg("Orchestrator event listener shut down with error")
		o.stateChan <- StateError
	}
}

func (o *Orchestrator) Teardown(ctx context.Context) error {
	o.cancelFunc()
	var allErrors []string
	if err := o.TeardownCloudRunService(ctx, o.arch.ServiceManagerSpec.Name); err != nil {
		allErrors = append(allErrors, fmt.Sprintf("failed to tear down ServiceDirector service: %v", err))
	}
	for dataflowName := range o.arch.Dataflows {
		if err := o.TeardownDataflowServices(ctx, dataflowName); err != nil {
			allErrors = append(allErrors, fmt.Sprintf("failed to tear down services for dataflow '%s': %v", dataflowName, err))
		}
	}
	if o.commandTopic != nil {
		if err := o.commandTopic.Delete(ctx); err != nil {
			allErrors = append(allErrors, fmt.Sprintf("failed to delete command topic: %v", err))
		}
	}
	if o.completionSubscription != nil {
		if err := o.completionSubscription.Delete(ctx); err != nil {
			allErrors = append(allErrors, fmt.Sprintf("failed to delete completion subscription: %v", err))
		}
	}
	if o.completionTopic != nil {
		if err := o.completionTopic.Delete(ctx); err != nil {
			allErrors = append(allErrors, fmt.Sprintf("failed to delete completion topic: %v", err))
		}
	}
	if o.psClient != nil {
		_ = o.psClient.Close()
	}
	if len(allErrors) > 0 {
		return fmt.Errorf("orchestrator cleanup encountered errors: %s", strings.Join(allErrors, "; "))
	}
	return nil
}
func (o *Orchestrator) TeardownCloudRunService(ctx context.Context, serviceName string) error {
	return o.deployer.Teardown(ctx, serviceName)
}
func (o *Orchestrator) TeardownDataflowServices(ctx context.Context, dataflowName string) error {
	o.archMutex.RLock()
	dataflow, ok := o.arch.Dataflows[dataflowName]
	o.archMutex.RUnlock()
	if !ok {
		return nil
	}
	if len(dataflow.Services) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(dataflow.Services))
	for serviceName := range dataflow.Services {
		wg.Add(1)
		go func(sn string) {
			defer wg.Done()
			if err := o.TeardownCloudRunService(ctx, sn); err != nil {
				errs <- err
			}
		}(serviceName)
	}
	wg.Wait()
	close(errs)
	var teardownErrors []string
	for err := range errs {
		teardownErrors = append(teardownErrors, err.Error())
	}
	if len(teardownErrors) > 0 {
		return fmt.Errorf("encountered %d error(s) during dataflow teardown: %s", len(teardownErrors), strings.Join(teardownErrors, "; "))
	}
	return nil
}
func (o *Orchestrator) DeployServiceDirector(ctx context.Context, saEmails map[string]string) (string, error) {
	serviceName := o.arch.ServiceManagerSpec.Name
	saEmail, ok := saEmails[serviceName]
	if !ok {
		return "", fmt.Errorf("SA email not found for ServiceDirector '%s'", serviceName)
	}
	deploymentSpec := o.arch.ServiceManagerSpec.Deployment
	if deploymentSpec == nil {
		return "", errors.New("ServiceManagerSpec.Deployment is not defined")
	}

	// make sure there is a waiter channel for service director
	_ = o.getWaiterChannel(ServiceDirector)

	return o.deployer.BuildAndDeploy(ctx, serviceName, saEmail, *deploymentSpec)
}
func (o *Orchestrator) BuildDataflowServices(ctx context.Context, dataflowName string) (map[string]string, error) {
	o.archMutex.RLock()
	dataflow, ok := o.arch.Dataflows[dataflowName]
	o.archMutex.RUnlock()
	if !ok {
		return nil, nil
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
		go func(sn string, spec servicemanager.ServiceSpec) {
			defer wg.Done()
			imageURI, err := o.deployer.Build(ctx, sn, *spec.Deployment)
			if err != nil {
				errs <- fmt.Errorf("failed to build '%s': %w", sn, err)
				return
			}
			results <- struct {
				serviceName string
				imageURI    string
			}{sn, imageURI}
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
		return nil, fmt.Errorf("encountered %d error(s) during build: %s", len(buildErrors), strings.Join(buildErrors, "; "))
	}
	builtImages := make(map[string]string)
	for result := range results {
		builtImages[result.serviceName] = result.imageURI
	}
	return builtImages, nil
}

// DeployDataflowServices deploys all services for a given dataflow and returns their URLs.
func (o *Orchestrator) DeployDataflowServices(ctx context.Context, dataflowName string, saEmails map[string]string, builtImages map[string]string, directorURL string) (map[string]string, error) {
	o.archMutex.RLock()
	dataflow, ok := o.arch.Dataflows[dataflowName]
	o.archMutex.RUnlock()
	if !ok {
		return nil, nil
	}
	if len(dataflow.Services) == 0 {
		return nil, nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(dataflow.Services))
	// REFACTOR: Create a channel to receive the URLs of successfully deployed services.
	urlChan := make(chan struct {
		Name string
		URL  string
	}, len(dataflow.Services))

	for sName, sSpec := range dataflow.Services {
		if sSpec.Deployment == nil {
			continue
		}
		saEmail, ok := saEmails[sName]
		if !ok {
			return nil, fmt.Errorf("SA email not found for '%s'", sName)
		}
		imageURI, ok := builtImages[sName]
		if !ok {
			return nil, fmt.Errorf("image URI not found for '%s'", sName)
		}
		wg.Add(1)
		go func(sn string, spec servicemanager.ServiceSpec, sa, img string) {
			defer wg.Done()
			localSpec := *spec.Deployment
			localSpec.Image = img
			if localSpec.EnvironmentVars == nil {
				localSpec.EnvironmentVars = make(map[string]string)
			}
			localSpec.EnvironmentVars["SERVICE_DIRECTOR_URL"] = directorURL
			localSpec.EnvironmentVars["PROJECT_ID"] = o.arch.ProjectID
			deployedURL, err := o.deployer.DeployService(ctx, sn, sa, localSpec)
			if err != nil {
				errs <- fmt.Errorf("failed to deploy '%s': %w", sn, err)
				return
			}
			// REFACTOR: Send the successful result to the URL channel.
			urlChan <- struct {
				Name string
				URL  string
			}{sn, deployedURL}
		}(sName, sSpec, saEmail, imageURI)
	}
	wg.Wait()
	close(errs)
	close(urlChan)

	var deploymentErrors []string
	for err := range errs {
		deploymentErrors = append(deploymentErrors, err.Error())
	}
	if len(deploymentErrors) > 0 {
		return nil, fmt.Errorf("encountered %d error(s) during deployment: %s", len(deploymentErrors), strings.Join(deploymentErrors, "; "))
	}

	// REFACTOR: Collect the results from the channel into the map to be returned.
	deployedURLs := make(map[string]string)
	for res := range urlChan {
		deployedURLs[res.Name] = res.URL
	}
	return deployedURLs, nil
}

func (o *Orchestrator) TriggerDataflowResourceCreation(ctx context.Context, dataflowName string) error {
	payload := FoundationalSetupPayload{DataflowName: dataflowName}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal foundational setup payload: %w", err)
	}

	cmd := Command{Instruction: Setup, Payload: payloadBytes}
	cmdMsg, err := json.Marshal(cmd)

	if err != nil {
		return fmt.Errorf("failed to marshal setup command: %w", err)
	}
	result := o.commandTopic.Publish(ctx, &pubsub.Message{Data: cmdMsg})
	_, err = result.Get(ctx)
	return err
}

// TriggerDependentResourceCreation sends the command to set up dependent resources.
func (o *Orchestrator) TriggerDependentResourceCreation(ctx context.Context, dataflowName string, serviceURLs map[string]string) error {
	payload := DependentSetupPayload{
		DataflowName: dataflowName,
		ServiceURLs:  serviceURLs,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal dependent setup payload: %w", err)
	}

	cmd := Command{Instruction: SetupDependent, Payload: payloadBytes}
	cmdMsg, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal dependent setup command: %w", err)
	}

	result := o.commandTopic.Publish(ctx, &pubsub.Message{Data: cmdMsg})
	_, err = result.Get(ctx)
	return err
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
