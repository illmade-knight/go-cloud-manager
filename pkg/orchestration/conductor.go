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

// ConductorOptions provides flags to control the orchestration workflow.
type ConductorOptions struct {
	CheckPrerequisites     bool
	SetupIAM               bool // REFACTOR: Consolidated IAM setup into one flag
	BuildAndDeployServices bool // REFACTOR: Consolidated build/deploy into one flag
	TriggerRemoteSetup     bool
	VerifyDataflowIAM      bool
	DirectorURLOverride    string
	VerificationTimeout    time.Duration
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
func NewConductor(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, opts ConductorOptions) (*Conductor, error) {
	// Assuming NewGoogleServiceAPIClient and other constructors are correctly implemented
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

// REFACTOR: The Run method is re-architected to follow the correct, final workflow.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration...")

	if err := c.checkPrerequisites(ctx); err != nil {
		return err
	}

	// Phase 1: Set up all necessary IAM service accounts upfront.
	if err := c.setupIAMPhase(ctx); err != nil {
		return err
	}

	// Phase 2: Run builds in parallel with ServiceDirector deployment and triggering.
	if err := c.buildAndRemoteSetupPhase(ctx); err != nil {
		return err
	}

	// Phase 3: Verify the IAM policies that were applied by the remote ServiceDirector.
	if err := c.verificationPhase(ctx); err != nil {
		return err
	}

	// Phase 4: Deploy the final application services with the pre-built images.
	if err := c.deploymentPhase(ctx); err != nil {
		return err
	}

	c.logger.Info().Msg("✅ Full architecture orchestration completed successfully.")
	return nil
}

func (c *Conductor) checkPrerequisites(ctx context.Context) error {
	if !c.options.CheckPrerequisites {
		c.logger.Info().Msg("Phase 0: Skipping prerequisite API check.")
		return nil
	}
	c.logger.Info().Msg("Executing Phase 0: Checking prerequisite service APIs...")
	// This now calls a simplified CheckAndEnable that doesn't need the full architecture
	err := c.prerequisitesManager.CheckAndEnable(ctx, c.arch)
	if err != nil {
		return fmt.Errorf("failed during prerequisite API check: %w", err)
	}
	c.logger.Info().Msg("Phase 0: Prerequisite API check complete.")
	return nil
}

// REFACTOR: This new phase handles all Service Account creation.
func (c *Conductor) setupIAMPhase(ctx context.Context) error {
	if !c.options.SetupIAM {
		c.logger.Info().Msg("Phase 1: Skipping all IAM setup.")
		return nil
	}
	c.logger.Info().Msg("Executing Phase 1: Setting up all service accounts...")

	// First, set up the ServiceDirector's own SA and permissions.
	sdSaEmails, err := c.iamOrch.SetupServiceDirectorIAM(ctx)
	if err != nil {
		return err
	}
	for k, v := range sdSaEmails {
		c.serviceAccountEmails[k] = v
	}

	// Then, ensure all application SAs exist.
	for dataflowName := range c.arch.Dataflows {
		dfSaEmails, err := c.iamOrch.EnsureDataflowSAsExist(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed during application service SA setup for dataflow '%s': %w", dataflowName, err)
		}
		for k, v := range dfSaEmails {
			c.serviceAccountEmails[k] = v
		}
	}
	c.logger.Info().Msg("Phase 1: All service accounts are ready.")
	return nil
}

// REFACTOR: This phase now runs builds in parallel with deploying and triggering the director.
func (c *Conductor) buildAndRemoteSetupPhase(ctx context.Context) error {
	if !c.options.BuildAndDeployServices {
		c.logger.Info().Msg("Phase 2: Skipping build and remote setup.")
		return nil
	}
	c.logger.Info().Msg("Executing Phase 2: Building services and triggering remote setup in parallel...")

	var wg sync.WaitGroup
	errChan := make(chan error, 2)

	// Task A: Build all dataflow service images.
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.logger.Info().Msg("Starting build of all dataflow services...")
		for dataflowName := range c.arch.Dataflows {
			builtImages, err := c.orch.BuildDataflowServices(ctx, dataflowName)
			if err != nil {
				errChan <- fmt.Errorf("failed to build services for dataflow '%s': %w", dataflowName, err)
				return
			}
			c.imageBuildMutex.Lock()
			for service, image := range builtImages {
				c.builtImageURIs[service] = image
			}
			c.imageBuildMutex.Unlock()
		}
		c.logger.Info().Msg("Build of all dataflow services is complete.")
	}()

	// Task B: Deploy the ServiceDirector and trigger it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.deployServiceDirector(ctx); err != nil {
			errChan <- err
			return
		}
		if err := c.triggerRemoteSetup(ctx); err != nil {
			errChan <- err
			return
		}
	}()

	wg.Wait()
	close(errChan)

	var phaseErrors []error
	for err := range errChan {
		phaseErrors = append(phaseErrors, err)
	}
	if len(phaseErrors) > 0 {
		return fmt.Errorf("encountered errors during build and remote setup phase: %v", phaseErrors)
	}

	c.logger.Info().Msg("Phase 2: Build and remote setup completed successfully.")
	return nil
}

func (c *Conductor) deployServiceDirector(ctx context.Context) error {
	c.logger.Info().Msg("Deploying ServiceDirector...")
	if c.options.DirectorURLOverride != "" {
		c.directorURL = c.options.DirectorURLOverride
		c.logger.Info().Str("url", c.directorURL).Msg("Using provided Director URL override.")
		return nil
	}

	if _, ok := c.serviceAccountEmails[c.arch.ServiceManagerSpec.Name]; !ok {
		return errors.New("cannot deploy service director: SA email not found from IAM setup phase")
	}

	url, err := c.orch.DeployServiceDirector(ctx, c.serviceAccountEmails)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector deployment: %w", err)
	}
	c.directorURL = url

	c.logger.Info().Str("url", url).Msg("Waiting for ServiceDirector to become ready...")
	err = c.orch.AwaitServiceReady(ctx, c.arch.ServiceManagerSpec.Name)
	if err != nil {
		return fmt.Errorf("failed while waiting for ServiceDirector to become ready: %w", err)
	}
	c.logger.Info().Msg("ServiceDirector is deployed and ready.")
	return nil
}

func (c *Conductor) triggerRemoteSetup(ctx context.Context) error {
	if !c.options.TriggerRemoteSetup {
		c.logger.Info().Msg("Skipping remote resource setup trigger.")
		return nil
	}
	c.logger.Info().Msg("Triggering remote dataflow resource and IAM setup...")
	for dataflowName := range c.arch.Dataflows {
		err := c.orch.TriggerDataflowResourceCreation(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed to trigger setup for dataflow '%s': %w", dataflowName, err)
		}
		err = c.orch.AwaitDataflowReady(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed to receive completion for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Remote dataflow resource and IAM setup is complete.")
	return nil
}

func (c *Conductor) verificationPhase(ctx context.Context) error {
	if !c.options.VerifyDataflowIAM {
		c.logger.Info().Msg("Phase 3: Skipping dataflow IAM verification.")
		return nil
	}
	c.logger.Info().Msg("Executing Phase 3: Verifying dataflow IAM policy propagation...")
	for dataflowName := range c.arch.Dataflows {
		err := c.iamOrch.VerifyIAMForDataflow(ctx, dataflowName, c.options.VerificationTimeout)
		if err != nil {
			return fmt.Errorf("failed during IAM verification for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Phase 3: Dataflow IAM verification complete.")
	return nil
}

func (c *Conductor) deploymentPhase(ctx context.Context) error {
	if !c.options.BuildAndDeployServices {
		c.logger.Info().Msg("Phase 4: Skipping final deployment of services.")
		return nil
	}
	c.logger.Info().Msg("Executing Phase 4: Deploying final application services...")
	if c.directorURL == "" {
		return errors.New("cannot deploy dataflow services: director URL is not available")
	}

	for dataflowName := range c.arch.Dataflows {
		err := c.orch.DeployDataflowServices(ctx, dataflowName, c.serviceAccountEmails, c.builtImageURIs, c.directorURL)
		if err != nil {
			return fmt.Errorf("failed to deploy application services for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Phase 4: Final deployment of all dataflow services is complete.")
	return nil
}

// Teardown cleans up all resources managed by the conductor.
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
