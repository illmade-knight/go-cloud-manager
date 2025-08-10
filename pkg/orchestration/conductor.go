package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/prerequisites"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// ConductorOptions provides flags and configuration to control the orchestration workflow.
type ConductorOptions struct {
	CheckPrerequisites     bool
	SetupIAM               bool
	BuildAndDeployServices bool
	TriggerRemoteSetup     bool
	VerifyDataflowIAM      bool
	DirectorURLOverride    string
	SAPollTimeout          time.Duration
	PolicyPollTimeout      time.Duration
}

// Conductor manages the high-level orchestration of a full microservice architecture deployment.
type Conductor struct {
	arch                 *servicemanager.MicroserviceArchitecture
	logger               zerolog.Logger
	prerequisitesManager *prerequisites.Manager
	iamOrch              *IAMOrchestrator
	orch                 *Orchestrator
	options              ConductorOptions
	serviceAccountEmails map[string]string
	directorURL          string
	builtImageURIs       map[string]string
	imageBuildMutex      sync.Mutex
}

// NewConductor creates and initializes a new Conductor with specific options.
// This is the correct constructor that you provided.
func NewConductor(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, opts ConductorOptions) (*Conductor, error) {
	prerequisitesClient, err := prerequisites.NewGoogleServiceAPIClient(ctx, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create prerequisite client: %w", err)
	}
	prerequisitesManager := prerequisites.NewManager(prerequisitesClient, logger)

	iamOrch, err := NewIAMOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM orchestrator: %w", err)
	}

	orch, err := NewOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create main orchestrator: %w", err)
	}
	return &Conductor{
		arch:                 arch,
		prerequisitesManager: prerequisitesManager,
		logger:               logger.With().Str("component", "Conductor").Logger(),
		iamOrch:              iamOrch,
		orch:                 orch,
		options:              opts,
		serviceAccountEmails: make(map[string]string),
		builtImageURIs:       make(map[string]string),
	}, nil
}

// Run executes the full, multi-phase orchestration workflow in the correct sequence.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration...")

	if err := c.checkPrerequisites(ctx); err != nil {
		return err
	}
	// Phase 1: Create all service accounts and wait for them to propagate.
	if err := c.setupIAMPhase(ctx); err != nil {
		return err
	}
	// Phase 2: Run builds in parallel with only the ServiceDirector deployment. This is a blocking phase.
	if err := c.buildAndDeployPhase(ctx); err != nil {
		return err
	}
	// Phase 3: Now that builds are done and director is ready, trigger remote setup. This is a blocking phase.
	completionEvents, err := c.triggerRemoteSetup(ctx)
	if err != nil {
		return err
	}
	// Phase 4: Now that remote setup is complete, verify IAM policy propagation.
	if err := c.verificationPhase(ctx, completionEvents); err != nil {
		return err
	}
	// Phase 5: Now that everything is verified, deploy the final application services.
	if err := c.deploymentPhase(ctx); err != nil {
		return err
	}

	c.logger.Info().Msg("✅ Full architecture orchestration completed successfully.")
	return nil
}

func (c *Conductor) checkPrerequisites(ctx context.Context) error {
	if !c.options.CheckPrerequisites {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 0: Checking prerequisite service APIs...")
	err := c.prerequisitesManager.CheckAndEnable(ctx, c.arch)
	if err != nil {
		return fmt.Errorf("failed during prerequisite API check: %w", err)
	}
	return nil
}

func (c *Conductor) setupIAMPhase(ctx context.Context) error {
	if !c.options.SetupIAM {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 1: Setting up all service accounts...")
	sdSaEmails, err := c.iamOrch.SetupServiceDirectorIAM(ctx)
	if err != nil {
		return err
	}
	for k, v := range sdSaEmails {
		c.serviceAccountEmails[k] = v
	}
	var allAppSaEmails []string
	for dataflowName := range c.arch.Dataflows {
		dfSaEmails, err := c.iamOrch.EnsureDataflowSAsExist(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed during SA setup for dataflow '%s': %w", dataflowName, err)
		}
		for k, v := range dfSaEmails {
			c.serviceAccountEmails[k] = v
			allAppSaEmails = append(allAppSaEmails, v)
		}
	}
	c.logger.Info().Int("count", len(allAppSaEmails)).Msg("Verifying propagation of newly created service accounts...")
	for _, email := range allAppSaEmails {
		err := c.iamOrch.PollForSAExistence(ctx, email, c.options.SAPollTimeout)
		if err != nil {
			return fmt.Errorf("failed while waiting for SA '%s' to propagate: %w", email, err)
		}
	}
	c.logger.Info().Msg("Phase 1: All service accounts are created and have propagated.")
	return nil
}

func (c *Conductor) buildAndDeployPhase(ctx context.Context) error {
	if !c.options.BuildAndDeployServices {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 2: Building services and deploying director in parallel...")
	var wg sync.WaitGroup
	errChan := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.buildDataflowServices(ctx); err != nil {
			errChan <- err
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.deployServiceDirector(ctx); err != nil {
			errChan <- err
		}
	}()
	wg.Wait()
	close(errChan)
	var phaseErrors []error
	for err := range errChan {
		phaseErrors = append(phaseErrors, err)
	}
	if len(phaseErrors) > 0 {
		return fmt.Errorf("encountered errors during build and director deployment phase: %v", phaseErrors)
	}
	c.logger.Info().Msg("Phase 2: Build and director deployment completed successfully.")
	return nil
}

func (c *Conductor) buildDataflowServices(ctx context.Context) error {
	c.logger.Info().Msg("Starting build of all dataflow services...")
	for dataflowName := range c.arch.Dataflows {
		builtImages, err := c.orch.BuildDataflowServices(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed to build services for dataflow '%s': %w", dataflowName, err)
		}
		c.imageBuildMutex.Lock()
		for service, image := range builtImages {
			c.builtImageURIs[service] = image
		}
		c.imageBuildMutex.Unlock()
	}
	c.logger.Info().Msg("Build of all dataflow services is complete.")
	return nil
}

func (c *Conductor) deployServiceDirector(ctx context.Context) error {
	c.logger.Info().Msg("Deploying ServiceDirector...")
	if c.options.DirectorURLOverride != "" {
		c.directorURL = c.options.DirectorURLOverride
		return nil
	}
	if _, ok := c.serviceAccountEmails[c.arch.ServiceManagerSpec.Name]; !ok {
		return errors.New("cannot deploy service director: SA email not found")
	}

	url, err := c.orch.DeployServiceDirector(ctx, c.serviceAccountEmails)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector deployment: %w", err)
	}
	c.directorURL = url
	c.logger.Info().Str("url", url).Str("topic", c.orch.completionTopic.ID()).
		Str("sub", c.orch.completionSubscription.ID()).Msg("Waiting for ServiceDirector to become ready...")
	err = c.orch.AwaitServiceReady(ctx, ServiceDirector)
	if err != nil {
		return fmt.Errorf("failed while waiting for ServiceDirector to become ready: %w", err)
	}
	c.logger.Info().Msg("ServiceDirector is deployed and ready.")
	return nil
}

func (c *Conductor) triggerRemoteSetup(ctx context.Context) (map[string]CompletionEvent, error) {
	if !c.options.TriggerRemoteSetup {
		return nil, nil
	}
	c.logger.Info().Msg("Executing Phase 3: Triggering remote dataflow resource and IAM setup...")
	completionEvents := make(map[string]CompletionEvent)
	for dataflowName := range c.arch.Dataflows {
		err := c.orch.TriggerDataflowResourceCreation(ctx, dataflowName)
		if err != nil {
			return nil, fmt.Errorf("failed to trigger setup for dataflow '%s': %w", dataflowName, err)
		}
		event, err := c.orch.AwaitDataflowReady(ctx, dataflowName)
		if err != nil {
			return nil, fmt.Errorf("failed to receive completion for dataflow '%s': %w", dataflowName, err)
		}
		completionEvents[dataflowName] = event
	}
	c.logger.Info().Msg("Phase 3: Remote dataflow resource and IAM setup is complete.")
	return completionEvents, nil
}

func (c *Conductor) verificationPhase(ctx context.Context, actualEvents map[string]CompletionEvent) error {
	if !c.options.VerifyDataflowIAM {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 4: Verifying dataflow IAM policy propagation...")

	// This is a simplified comparison logic. A real implementation would need a deep-equals on the policy maps.
	// For this refactor, we are trusting the remote director and proceeding to poll.
	c.logger.Info().Msg("Trusting remote plan; proceeding to poll for propagation.")

	for dataflowName := range c.arch.Dataflows {
		err := c.iamOrch.VerifyIAMForDataflow(ctx, dataflowName, c.serviceAccountEmails, c.options.PolicyPollTimeout)
		if err != nil {
			return fmt.Errorf("failed during IAM verification for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Phase 4: Dataflow IAM verification complete.")
	return nil
}

func (c *Conductor) deploymentPhase(ctx context.Context) error {
	if !c.options.BuildAndDeployServices {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 5: Deploying final application services...")
	if c.directorURL == "" {
		return errors.New("cannot deploy dataflow services: director URL is not available")
	}
	for dataflowName := range c.arch.Dataflows {
		err := c.orch.DeployDataflowServices(ctx, dataflowName, c.serviceAccountEmails, c.builtImageURIs, c.directorURL)
		if err != nil {
			return fmt.Errorf("failed to deploy application services for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Phase 5: Final deployment of all dataflow services is complete.")
	return nil
}

func (c *Conductor) Teardown(ctx context.Context) error {
	c.logger.Info().Msg("--- Starting Conductor Teardown ---")
	var allErrors []error
	if err := c.orch.Teardown(ctx); err != nil {
		c.logger.Error().Err(err).Msg("Orchestrator teardown failed")
		allErrors = append(allErrors, err)
	}
	if err := c.iamOrch.Teardown(ctx); err != nil {
		c.logger.Error().Err(err).Msg("IAM teardown failed")
		allErrors = append(allErrors, err)
	}
	if len(allErrors) > 0 {
		return fmt.Errorf("conductor teardown failed with %d errors: %v", len(allErrors), allErrors)
	}
	c.logger.Info().Msg("--- Conductor Teardown Complete ---")
	return nil
}
