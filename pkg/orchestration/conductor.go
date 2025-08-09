package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/illmade-knight/go-cloud-manager/pkg/prerequisites" // NEW: Import the new package
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// ConductorOptions provides flags to control the orchestration workflow.
type ConductorOptions struct {
	// Step Flags: Set to true to execute the step, false to skip.
	CheckPrerequisites      bool
	SetupServiceDirectorIAM bool
	DeployServiceDirector   bool
	VerifyDataflowIAM       bool
	SetupDataflowResources  bool
	// REFACTOR: The single ApplyDataflowIAM step is replaced by the Build and Deploy phases.
	BuildDataflowServices  bool // NEW
	DeployDataflowServices bool

	// DirectorURLOverride is required if DeployServiceDirector is false but DeployDataflowServices is true.
	DirectorURLOverride string
}

// Conductor manages the high-level orchestration of a full microservice architecture deployment.
// It executes a series of dependent phases, but can flexibly skip phases if the required
// information is provided via overrides.
type Conductor struct {
	arch                 *servicemanager.MicroserviceArchitecture
	logger               zerolog.Logger
	prerequisitesManager *prerequisites.Manager
	iamOrch              *IAMOrchestrator
	orch                 *Orchestrator
	options              ConductorOptions
	serviceAccountEmails map[string]string // Internal state for SA emails.
	directorURL          string            // Internal state for the director URL.
	// REFACTOR: Add state to hold the results of the parallel build phase.
	builtImageURIs  map[string]string
	imageBuildMutex sync.Mutex
}

// NewConductor creates and initializes a new Conductor with specific options.
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
		builtImageURIs:       make(map[string]string), // REFACTOR: Initialize new map
	}, nil
}

// Run method is re-architected to support a multi-phase, parallel workflow.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration with specified options...")

	if err := c.checkPrerequisites(ctx); err != nil {
		return err
	}

	// Phase 1: Run long-running tasks (builds and remote setup) in parallel.
	if err := c.buildAndSetupPhase(ctx); err != nil {
		return err
	}

	// Phase 2: Verify that all remote IAM policies have propagated.
	if err := c.verificationPhase(ctx); err != nil {
		return err
	}

	// Phase 3: Deploy the final service revisions using the pre-built images.
	if err := c.deploymentPhase(ctx); err != nil {
		return err
	}

	c.logger.Info().Msg("✅ Full architecture orchestration completed successfully.")
	return nil
}

// buildAndSetupPhase: This new method manages the parallel execution of the build and setup tasks.
func (c *Conductor) buildAndSetupPhase(ctx context.Context) error {
	var wg sync.WaitGroup
	errChan := make(chan error, 2) // One for each parallel task group.

	// Task A: Build all dataflow service images concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.buildDataflowServices(ctx); err != nil {
			errChan <- err
		}
	}()

	// Task B: Set up all remote infrastructure sequentially.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.setupServiceDirectorIAM(ctx); err != nil {
			errChan <- err
			return
		}
		if err := c.deployServiceDirector(ctx); err != nil {
			errChan <- err
			return
		}
		if err := c.setupDataflowResources(ctx); err != nil {
			errChan <- err
			return
		}
	}()

	c.logger.Info().Msg("Waiting for parallel build and setup phases to complete...")
	wg.Wait()
	close(errChan)

	var setupErrors []error
	for err := range errChan {
		setupErrors = append(setupErrors, err)
	}
	if len(setupErrors) > 0 {
		return fmt.Errorf("encountered errors during build and setup phase: %v", setupErrors)
	}

	c.logger.Info().Msg("Parallel build and setup phases completed successfully.")
	return nil
}

// REFACTOR: This new method represents the dedicated verification phase.
func (c *Conductor) verificationPhase(ctx context.Context) error {
	if !c.options.VerifyDataflowIAM {
		c.logger.Info().Msg("Skipping dataflow IAM verification phase.")
		return nil
	}
	c.logger.Info().Msg("Executing verification phase...")
	for dataflowName := range c.arch.Dataflows {
		err := c.iamOrch.VerifyIAMForDataflow(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed during IAM verification for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Verification phase complete.")
	return nil
}

// REFACTOR: This new method represents the final deployment phase.
func (c *Conductor) deploymentPhase(ctx context.Context) error {
	if !c.options.DeployDataflowServices {
		c.logger.Info().Msg("Skipping final deployment phase.")
		return nil
	}
	c.logger.Info().Msg("Executing final deployment phase...")
	// This step depends on the director URL from a previous step.
	if c.directorURL == "" {
		return errors.New("cannot deploy dataflow services: director URL is not available")
	}

	// Apply dataflow IAM for application services, which must be done before deployment.
	if err := c.applyDataflowIAM(ctx); err != nil {
		return err
	}

	for dataflowName := range c.arch.Dataflows {
		// Assumes 'orch' has a modified DeployDataflowServices that accepts the built images map.
		err := c.orch.DeployDataflowServices(ctx, dataflowName, c.serviceAccountEmails, c.builtImageURIs, c.directorURL)
		if err != nil {
			return fmt.Errorf("failed to deploy application services for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Final deployment phase complete.")
	return nil
}

// checkPrerequisites is phase 0 of the orchestration.
func (c *Conductor) checkPrerequisites(ctx context.Context) error {
	if !c.options.CheckPrerequisites {
		c.logger.Info().Msg("Skipping prerequisite API check.")
		return nil
	}
	c.logger.Info().Msg("Executing prerequisite API check...")
	err := c.prerequisitesManager.CheckAndEnable(ctx, c.arch)
	if err != nil {
		return fmt.Errorf("failed during prerequisite API check: %w", err)
	}
	c.logger.Info().Msg("Prerequisite API check complete.")
	return nil
}

// setupServiceDirectorIAM is part of the remote setup task.
func (c *Conductor) setupServiceDirectorIAM(ctx context.Context) error {
	if !c.options.SetupServiceDirectorIAM {
		c.logger.Info().Msg("Skipping ServiceDirector IAM setup.")
		return nil
	}
	c.logger.Info().Msg("Executing ServiceDirector IAM setup...")
	sdSaEmails, err := c.iamOrch.SetupServiceDirectorIAM(ctx)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector IAM setup: %w", err)
	}
	for k, v := range sdSaEmails {
		c.serviceAccountEmails[k] = v
	}
	c.logger.Info().Msg("ServiceDirector IAM setup complete.")
	return nil
}

// deployServiceDirector is part of the remote setup task.
func (c *Conductor) deployServiceDirector(ctx context.Context) error {
	if !c.options.DeployServiceDirector {
		c.logger.Info().Msg("Skipping ServiceDirector deployment.")
		if c.options.DeployDataflowServices {
			if c.options.DirectorURLOverride == "" {
				return errors.New("DeployServiceDirector was skipped, but DeployDataflowServices requires a DirectorURLOverride")
			}
			c.directorURL = c.options.DirectorURLOverride
			c.logger.Info().Str("url", c.directorURL).Msg("Using provided Director URL override.")
		}
		return nil
	}
	c.logger.Info().Msg("Executing ServiceDirector deployment...")
	if _, ok := c.serviceAccountEmails[c.arch.ServiceManagerSpec.Name]; !ok && c.options.SetupServiceDirectorIAM {
		return errors.New("cannot deploy service director: service account email was not found from the IAM setup phase")
	}

	url, err := c.orch.DeployServiceDirector(ctx, c.serviceAccountEmails)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector deployment: %w", err)
	}
	c.directorURL = url // Store the result in the conductor's state.

	c.logger.Info().Str("url", url).Msg("Waiting for ServiceDirector to become ready...")
	err = c.orch.AwaitServiceReady(ctx, c.arch.ServiceManagerSpec.Name)
	if err != nil {
		return fmt.Errorf("failed while waiting for ServiceDirector to become ready: %w", err)
	}

	c.logger.Info().Msg("ServiceDirector is deployed and ready.")
	return nil
}

// setupDataflowResources is part of the remote setup task.
// REFACTOR: This now assumes the remote ServiceDirector also handles applying initial IAM.
func (c *Conductor) setupDataflowResources(ctx context.Context) error {
	if !c.options.SetupDataflowResources {
		c.logger.Info().Msg("Skipping dataflow resource and IAM setup.")
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

// REFACTOR: This is a new method for the build phase.
func (c *Conductor) buildDataflowServices(ctx context.Context) error {
	if !c.options.BuildDataflowServices {
		c.logger.Info().Msg("Skipping build of dataflow services.")
		return nil
	}
	c.logger.Info().Msg("Starting build of all dataflow services...")
	for dataflowName := range c.arch.Dataflows {
		// Assumes 'orch' has a new BuildDataflowServices method.
		builtImages, err := c.orch.BuildDataflowServices(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed to build application services for dataflow '%s': %w", dataflowName, err)
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

// applyDataflowIAM is now part of the final deployment phase.
func (c *Conductor) applyDataflowIAM(ctx context.Context) error {
	c.logger.Info().Msg("Applying application service IAM...")
	for dataflowName := range c.arch.Dataflows {
		dfSaEmails, err := c.iamOrch.ApplyIAMForDataflow(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed during application service IAM setup for dataflow '%s': %w", dataflowName, err)
		}
		// Merge the results into the conductor's state.
		for k, v := range dfSaEmails {
			c.serviceAccountEmails[k] = v
		}
	}
	c.logger.Info().Msg("Application service IAM setup is complete.")
	return nil
}

// Teardown cleans up all resources managed by the conductor.
func (c *Conductor) Teardown(ctx context.Context) error {
	c.logger.Info().Msg("--- Starting Conductor Teardown ---")
	var errs []error

	err := c.orch.Teardown(ctx)
	if err != nil {
		c.logger.Error().Err(err).Msg("Orchestrator teardown failed")
		errs = append(errs, err)
	}
	err = c.iamOrch.Teardown(ctx)
	if err != nil {
		c.logger.Error().Err(err).Msg("IAM teardown failed")
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("conductor teardown failed with %d errors", len(errs))
	}
	c.logger.Info().Msg("--- Conductor Teardown Complete ---")
	return nil
}
