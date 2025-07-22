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
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
)

// (eventWaiter and other type definitions remain the same)
// eventWaiter is a simple, thread-safe mechanism to allow multiple goroutines
// to wait for different, specific events originating
type eventWaiter struct {
	mu      sync.Mutex
	waiters map[CompletionStatus]chan struct{}
}

func newEventWaiter() *eventWaiter {
	return &eventWaiter{
		waiters: make(map[CompletionStatus]chan struct{}),
	}
}

// await returns a channel that a caller can wait on for a specific event key.
func (ew *eventWaiter) await(key CompletionStatus) <-chan struct{} {
	ew.mu.Lock()
	defer ew.mu.Unlock()
	ch, exists := ew.waiters[key]
	if !exists {
		ch = make(chan struct{})
		ew.waiters[key] = ch
	}
	return ch
}

// signal finds the channel for a given event and closes it. If a waiter isn't
// ready yet, it creates a closed channel to act as a signal for when the waiter arrives.
func (ew *eventWaiter) signal(key CompletionStatus) {
	ew.mu.Lock()
	defer ew.mu.Unlock()

	if ch, exists := ew.waiters[key]; exists {
		// A waiter is already here. Close the channel to unblock it.
		// Avoid panicking by closing a channel that might already be closed.
		select {
		case <-ch:
			// Channel is already closed, do nothing.
		default:
			close(ch)
		}
	} else {
		// No one is waiting yet. Create a CLOSED channel.
		// When await() runs, it will find this channel in the map and not block.
		closedCh := make(chan struct{})
		close(closedCh)
		ew.waiters[key] = closedCh
	}
}

type OrchestratorState string

const (
	StateInitializing             OrchestratorState = "INITIALIZING"
	StateCommandInfraReady        OrchestratorState = "COMMAND_INFRA_READY"
	StateDeployingServiceDirector OrchestratorState = "DEPLOYING_SERVICE_DIRECTOR"
	StateServiceDirectorBuilt     OrchestratorState = "SERVICE_DIRECTOR_BUILT"
	StateServiceDirectorReady     OrchestratorState = "SERVICE_DIRECTOR_READY"
	StateTriggeringDataflow       OrchestratorState = "TRIGGERING_DATAFLOW_SETUP"
	StateAwaitingCompletion       OrchestratorState = "AWAITING_DATAFLOW_COMPLETION"
	StateDataflowReady            OrchestratorState = "DATAFLOW_READY"
	StateError                    OrchestratorState = "ERROR"
)

type Command struct {
	Instruction CommandInstruction `json:"instruction"`
	Value       string             `json:"value"`
}

type CommandInstruction string

const (
	Setup    CommandInstruction = "dataflow-setup"
	Teardown CommandInstruction = "teardown"
)

type CompletionEvent struct {
	Status       CompletionStatus `json:"status"` // "success" or "failure"
	Value        string           `json:"value"`
	ErrorMessage string           `json:"error_message,omitempty"`
}

type CompletionStatus string

const (
	ServiceDirectorReady CompletionStatus = "setup_complete"
	DataflowComplete     CompletionStatus = "dataflow_complete"
)

type OrchestratorResources struct {
	commandTopic           *pubsub.Topic
	completionTopic        *pubsub.Topic
	completionSubscription *pubsub.Subscription
}

// Orchestrator manages the deployment and verification of entire dataflows.
type Orchestrator struct {
	archMutex   sync.Mutex
	arch        *servicemanager.MicroserviceArchitecture
	logger      zerolog.Logger
	deployer    *deployment.CloudBuildDeployer
	psClient    *pubsub.Client
	eventWaiter *eventWaiter
	stateChan   chan OrchestratorState
	closeOnce   sync.Once
	cancelFunc  context.CancelFunc

	*OrchestratorResources
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

	// Create a cancellable context for the orchestrator's internal goroutines.
	internalCtx, cancel := context.WithCancel(ctx)

	orch := &Orchestrator{
		arch:                  arch,
		logger:                logger,
		deployer:              deployer,
		psClient:              psClient,
		eventWaiter:           newEventWaiter(),
		stateChan:             make(chan OrchestratorState, 10),
		OrchestratorResources: &OrchestratorResources{},
		cancelFunc:            cancel,
	}

	go orch.initialize(internalCtx)

	return orch, nil
}

// DeployServiceDirector now takes the map of service accounts.
func (o *Orchestrator) DeployServiceDirector(ctx context.Context, saEmails map[string]string) (string, error) {
	o.stateChan <- StateDeployingServiceDirector
	o.logger.Info().Msg("Starting ServiceDirector deployment...")

	serviceName := o.arch.ServiceManagerSpec.Name
	saEmail, ok := saEmails[serviceName]
	if !ok {
		return "", fmt.Errorf("service account email not found for ServiceDirector '%s'", serviceName)
	}

	deploymentSpec := o.arch.ServiceManagerSpec.Deployment
	if deploymentSpec == nil {
		return "", errors.New("ServiceManagerSpec.Deployment is not defined in the architecture")
	}

	serviceURL, err := o.deployer.Deploy(ctx, serviceName, saEmail, *deploymentSpec)
	if err != nil {
		o.stateChan <- StateError
		return "", fmt.Errorf("servicedirector deployment failed: %w", err)
	}
	o.logger.Info().Str("url", serviceURL).Msg("ServiceDirector deploy returned")

	o.stateChan <- StateServiceDirectorBuilt
	return serviceURL, nil
}

// Define a struct to hold the result of a single deployment.
type deploymentResult struct {
	serviceName string
	serviceURL  string
}

func (o *Orchestrator) DeployDataflowServices(ctx context.Context, dataflowName string, saEmails map[string]string, directorURL string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Beginning parallel deployment of all application services...")

	dataflow, ok := o.arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow '%s' not found in architecture definition", dataflowName)
	}

	if len(dataflow.Services) == 0 {
		o.logger.Info().Str("dataflow", dataflowName).Msg("No application services defined, skipping.")
		return nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(dataflow.Services))

	// 1. Create a channel to receive successful deployment results.
	results := make(chan deploymentResult, len(dataflow.Services))

	for sName, sSpec := range dataflow.Services {
		// Shadow variables for the goroutine.
		serviceName := sName
		serviceSpec := sSpec

		deploymentSpec := serviceSpec.Deployment
		if deploymentSpec == nil {
			o.logger.Warn().Str("service", serviceName).Msg("Service has no deployment spec, skipping.")
			continue
		}

		saEmail, ok := saEmails[serviceName]
		if !ok {
			return fmt.Errorf("service account email not found for service '%s'", serviceName)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			o.logger.Info().Str("service", serviceName).Msg("Starting deployment...")

			// Create a safe local copy of the spec for this deployment.
			localSpec := *deploymentSpec
			localSpec.EnvironmentVars = make(map[string]string)
			for k, v := range deploymentSpec.EnvironmentVars {
				localSpec.EnvironmentVars[k] = v
			}
			localSpec.EnvironmentVars["SERVICE_DIRECTOR_URL"] = directorURL
			localSpec.EnvironmentVars["PROJECT_ID"] = o.arch.ProjectID

			serviceURL, err := o.deployer.Deploy(ctx, serviceName, saEmail, localSpec)
			if err != nil {
				errs <- fmt.Errorf("failed to deploy service '%s': %w", serviceName, err)
				return
			}

			// 2. On success, send the result back to the main thread via the channel.
			results <- deploymentResult{serviceName: serviceName, serviceURL: serviceURL}

			o.logger.Info().Str("service", serviceName).Str("url", serviceURL).Msg("Deployment successful.")
		}()
	}

	o.logger.Info().Msg("Waiting for all service deployments to complete...")
	wg.Wait()
	close(errs)
	// 3. Close the results channel after all goroutines are done.
	close(results)
	o.logger.Info().Msg("All service deployments have completed.")

	// 4. Safely process the results to update the main architecture spec.
	for result := range results {
		o.archMutex.Lock()
		if svc, ok := o.arch.Dataflows[dataflowName].Services[result.serviceName]; ok {
			svc.Deployment.ServiceURL = result.serviceURL
		}
		o.archMutex.Unlock()
	}

	// Collect and aggregate any errors.
	var deploymentErrors []string
	for err := range errs {
		deploymentErrors = append(deploymentErrors, err.Error())
	}

	if len(deploymentErrors) > 0 {
		return fmt.Errorf("encountered %d error(s) during deployment: %s", len(deploymentErrors), strings.Join(deploymentErrors, "; "))
	}

	return nil
}

func (o *Orchestrator) StateChan() <-chan OrchestratorState {
	return o.stateChan
}

func (o *Orchestrator) Teardown(ctx context.Context) error {
	o.logger.Info().Msg("Starting Orchestrator cleanup...")
	var errs []error
	if o.cancelFunc != nil {
		o.cancelFunc()
		o.logger.Info().Msg("Orchestrator internal context cancelled.")
	}
	time.Sleep(2 * time.Second)
	if o.commandTopic != nil {
		o.logger.Info().Str("topic", o.commandTopic.ID()).Msg("Deleting command topic...")
		if err := o.commandTopic.Delete(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete command topic %s: %w", o.commandTopic.ID(), err))
		}
	}
	if o.completionTopic != nil {
		o.logger.Info().Str("topic", o.completionTopic.ID()).Msg("Deleting completion topic...")
		if err := o.completionTopic.Delete(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete completion topic %s: %w", o.completionTopic.ID(), err))
		}
	}
	if o.completionSubscription != nil {
		o.logger.Info().Str("subscription", o.completionSubscription.ID()).Msg("Deleting completion subscription...")
		if err := o.completionSubscription.Delete(ctx); err != nil {
			o.logger.Warn().Err(err).Str("subscription", o.completionSubscription.ID()).Msg("Failed to delete completion subscription")
		}
	}
	o.closeOnce.Do(func() {
		close(o.stateChan)
		if o.psClient != nil {
			o.psClient.Close()
		}
	})
	if len(errs) > 0 {
		return fmt.Errorf("orchestrator cleanup encountered %d errors: %v", len(errs), errs)
	}
	o.logger.Info().Msg("Orchestrator cleanup finished.")
	return nil
}

func (o *Orchestrator) listenForEvents(ctx context.Context) {
	err := o.completionSubscription.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		o.logger.Info().Str("data", string(msg.Data)).Msg("Orchestrator received event")
		event := CompletionEvent{}
		if json.Unmarshal(msg.Data, &event) != nil {
			return
		}
		o.logger.Info().Str("status", string(event.Status)).Msg("got completion event")
		switch event.Status {
		case ServiceDirectorReady:
			o.eventWaiter.signal(ServiceDirectorReady)
		case DataflowComplete:
			o.eventWaiter.signal(DataflowComplete)
		default:
			o.stateChan <- StateError
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		o.logger.Error().Err(err).Msg("Orchestrator event listener shut down with error")
	}
}

func (o *Orchestrator) AwaitServiceReady(ctx context.Context, serviceName string) error {
	o.logger.Info().Str("service", serviceName).Str("status", string(ServiceDirectorReady)).Msg("Waiting for event...")
	o.stateChan <- StateAwaitingCompletion
	select {
	case <-o.eventWaiter.await(ServiceDirectorReady):
		o.logger.Info().Str("service", serviceName).Msg("Service readiness confirmed.")
		return nil
	case <-time.After(3 * time.Minute):
		return fmt.Errorf("timeout waiting for 'service_ready' event from '%s'", serviceName)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Orchestrator) AwaitDataflowReady(ctx context.Context, dataflowName string) error {
	o.logger.Info().Str("dataflow", dataflowName).Str("status", string(DataflowComplete)).Msg("Waiting for event...")
	o.stateChan <- StateAwaitingCompletion
	select {
	case <-o.eventWaiter.await(DataflowComplete):
		o.logger.Info().Str("dataflow", dataflowName).Msg("Dataflow setup confirmed.")
		o.stateChan <- StateDataflowReady
		return nil
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("timeout waiting for 'setup_complete' event for dataflow '%s'", dataflowName)
	case <-ctx.Done():
		return ctx.Err()
	}
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
	o.logger.Info().Msg("start listening for service director events")
	go o.listenForEvents(ctx)
	o.stateChan <- StateCommandInfraReady
}

func (o *Orchestrator) ensureCommandInfra(ctx context.Context) error {
	commandTopicID := o.arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMMAND_TOPIC"]
	commandTopic, err := ensureTopicExists(ctx, o.psClient, commandTopicID)
	if err != nil {
		return err
	}
	o.commandTopic = commandTopic
	completionTopicID := o.arch.ServiceManagerSpec.Deployment.EnvironmentVars["SD_COMPLETION_TOPIC"]
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

func (o *Orchestrator) TeardownServiceDirector(ctx context.Context, serviceName string) error {
	o.logger.Info().Str("service_name", serviceName).Msg("Tearing down ServiceDirector...")
	return o.deployer.Teardown(ctx, serviceName)
}

func (o *Orchestrator) TriggerDataflowSetup(ctx context.Context, dataflowName string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("dataflow setup")
	cmdPayload := Command{
		Instruction: Setup,
		Value:       "toy-dataflow",
	}
	cmdMsg, _ := json.Marshal(cmdPayload)
	result := o.commandTopic.Publish(ctx, &pubsub.Message{Data: cmdMsg})
	_, err := result.Get(ctx)
	return err
}

func (o *Orchestrator) AwaitRevisionReady(ctx context.Context, spec servicemanager.ServiceSpec) error {
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

// TeardownCloudRunService deletes a specific Cloud Run service.
// This is a more accurately named replacement for TeardownServiceDirector.
func (o *Orchestrator) TeardownCloudRunService(ctx context.Context, serviceName string) error {
	o.logger.Info().Str("service_name", serviceName).Msg("Tearing down Cloud Run service...")
	return o.deployer.Teardown(ctx, serviceName)
}

// TeardownDataflowServices tears down all services associated with a specific dataflow in parallel.
func (o *Orchestrator) TeardownDataflowServices(ctx context.Context, dataflowName string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Beginning parallel teardown of all application services...")

	o.archMutex.Lock()
	dataflow, ok := o.arch.Dataflows[dataflowName]
	o.archMutex.Unlock()

	if !ok {
		return fmt.Errorf("dataflow '%s' not found in architecture definition", dataflowName)
	}

	if len(dataflow.Services) == 0 {
		o.logger.Info().Str("dataflow", dataflowName).Msg("No application services defined, skipping teardown.")
		return nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(dataflow.Services))

	for serviceName := range dataflow.Services {
		// Shadow variable for the goroutine
		serviceNameToDelete := serviceName

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := o.TeardownCloudRunService(ctx, serviceNameToDelete); err != nil {
				// Send any errors to the channel instead of returning.
				errs <- err
			}
		}()
	}

	// Wait for all teardowns to finish.
	wg.Wait()
	close(errs)

	// Collect and aggregate any errors.
	var teardownErrors []string
	for err := range errs {
		teardownErrors = append(teardownErrors, err.Error())
	}

	if len(teardownErrors) > 0 {
		return fmt.Errorf("encountered %d error(s) during dataflow teardown: %s", len(teardownErrors), strings.Join(teardownErrors, "; "))
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("Successfully tore down all dataflow services.")
	return nil
}

// NewOrchestratorWithClients creates a new Orchestrator with pre-existing clients,
// which is useful for unit testing with mocks.
func NewOrchestratorWithClients(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, psClient *pubsub.Client, deployer *deployment.CloudBuildDeployer, logger zerolog.Logger) (*Orchestrator, error) {
	if arch.ProjectID == "" {
		return nil, errors.New("ProjectID cannot be empty in the architecture definition")
	}

	internalCtx, cancel := context.WithCancel(ctx)

	orch := &Orchestrator{
		arch:                  arch,
		logger:                logger,
		psClient:              psClient,
		deployer:              deployer,
		eventWaiter:           newEventWaiter(),
		stateChan:             make(chan OrchestratorState, 10),
		OrchestratorResources: &OrchestratorResources{},
		cancelFunc:            cancel,
	}

	go orch.initialize(internalCtx)

	return orch, nil
}
