package orchestration

import (
	"context"
	"errors"
	"fmt"

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
	ApplyDataflowIAM        bool
	DeployDataflowServices  bool

	// DirectorURLOverride is required if DeployServiceDirector is false but DeployDataflowServices is true.
	DirectorURLOverride string
}

// Conductor manages the high-level orchestration of a full microservice architecture deployment.
// It executes a series of dependent phases, but can flexibly skip phases if the required
// information is provided via overrides.
type Conductor struct {
	arch                 *servicemanager.MicroserviceArchitecture
	logger               zerolog.Logger
	prerequisitesManager *prerequisites.Manager // NEW: Add the prerequisite manager
	iamOrch              *IAMOrchestrator
	orch                 *Orchestrator
	options              ConductorOptions
	serviceAccountEmails map[string]string // Internal state for SA emails.
	directorURL          string            // Internal state for the director URL.
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
	}, nil
}

// Run executes the orchestration workflow according to the configured options.
// It calls each phase method in sequence, respecting the options to skip steps.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration with specified options...")

	var err error

	if err = c.checkPrerequisites(ctx); err != nil {
		return err
	}

	err = c.setupServiceDirectorIAM(ctx)
	if err != nil {
		return err
	}

	err = c.deployServiceDirector(ctx)
	if err != nil {
		return err
	}

	err = c.setupDataflowResources(ctx)
	if err != nil {
		return err
	}

	err = c.applyDataflowIAM(ctx)
	if err != nil {
		return err
	}

	err = c.verifyDataflowIAM(ctx)
	if err != nil {
		return err
	}

	err = c.deployDataflowServices(ctx)
	if err != nil {
		return err
	}

	c.logger.Info().Msg("✅ Full architecture orchestration completed successfully.")
	return nil
}

// checkPrerequisites is the new phase 0 of the orchestration.
func (c *Conductor) checkPrerequisites(ctx context.Context) error {
	if !c.options.CheckPrerequisites {
		c.logger.Info().Msg("Step 0: Skipping prerequisite API check.")
		return nil
	}
	c.logger.Info().Msg("Executing Step 0: Checking prerequisite service APIs...")
	err := c.prerequisitesManager.CheckAndEnable(ctx, c.arch)
	if err != nil {
		return fmt.Errorf("failed during prerequisite API check: %w", err)
	}
	c.logger.Info().Msg("Step 0: Prerequisite API check complete.")
	return nil
}

func (c *Conductor) verifyDataflowIAM(ctx context.Context) error {
	if !c.options.VerifyDataflowIAM {
		c.logger.Info().Msg("Step 4.5: Skipping dataflow IAM verification.")
		return nil
	}
	c.logger.Info().Msg("Executing Step 4.5: Verifying dataflow IAM policy propagation...")
	for dataflowName := range c.arch.Dataflows {
		err := c.iamOrch.VerifyIAMForDataflow(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed during IAM verification for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Step 4.5: Dataflow IAM verification complete.")
	return nil
}

// setupServiceDirectorIAM is phase 1 of the orchestration.
func (c *Conductor) setupServiceDirectorIAM(ctx context.Context) error {
	if !c.options.SetupServiceDirectorIAM {
		c.logger.Info().Msg("Step 1: Skipping ServiceDirector IAM setup.")
		return nil
	}

	c.logger.Info().Msg("Executing Step 1: Setting up ServiceDirector IAM...")
	sdSaEmails, err := c.iamOrch.SetupServiceDirectorIAM(ctx)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector IAM setup: %w", err)
	}
	// Store the result in the conductor's state.
	for k, v := range sdSaEmails {
		c.serviceAccountEmails[k] = v
	}
	c.logger.Info().Msg("Step 1: ServiceDirector IAM setup complete.")
	return nil
}

// deployServiceDirector is phase 2 of the orchestration.
func (c *Conductor) deployServiceDirector(ctx context.Context) error {
	if !c.options.DeployServiceDirector {
		c.logger.Info().Msg("Step 2: Skipping ServiceDirector deployment.")
		// If we skip deployment but need the URL later, we must use the override.
		if c.options.DeployDataflowServices {
			if c.options.DirectorURLOverride == "" {
				return errors.New("DeployServiceDirector was skipped, but DeployDataflowServices requires a DirectorURLOverride")
			}
			c.directorURL = c.options.DirectorURLOverride
			c.logger.Info().Str("url", c.directorURL).Msg("Using provided Director URL override for subsequent steps.")
		}
		return nil
	}

	c.logger.Info().Msg("Executing Step 2: Deploying ServiceDirector...")
	// This step depends on the SA email from the previous step.
	if _, ok := c.serviceAccountEmails[c.arch.ServiceManagerSpec.Name]; !ok && c.options.SetupServiceDirectorIAM {
		return errors.New("cannot deploy service director: service account email was not found from the IAM setup phase")
	}

	url, err := c.orch.DeployServiceDirector(ctx, c.serviceAccountEmails)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector deployment: %w", err)
	}
	c.logger.Info().Str("url", url).Msg("ServiceDirector deployment is complete.")
	c.arch.ServiceManagerSpec.Deployment.ServiceURL = url
	err = c.orch.AwaitServiceReady(ctx, c.arch.ServiceManagerSpec.Name)
	if err != nil {
		return fmt.Errorf("failed while waiting for ServiceDirector to become ready: %w", err)
	}

	c.directorURL = url // Store the result in the conductor's state.
	c.logger.Info().Str("url", c.directorURL).Msg("Step 2: ServiceDirector is deployed and ready.")
	return nil
}

// setupDataflowResources is phase 3 of the orchestration.
func (c *Conductor) setupDataflowResources(ctx context.Context) error {
	if !c.options.SetupDataflowResources {
		c.logger.Info().Msg("Step 3: Skipping dataflow resource setup.")
		return nil
	}

	c.logger.Info().Msg("Executing Step 3: Setting up dataflow resources...")
	for dataflowName := range c.arch.Dataflows {
		c.logger.Info().Str("dataflow", dataflowName).Msg("Triggering resource setup...")
		err := c.orch.TriggerDataflowResourceCreation(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed to trigger setup for dataflow '%s': %w", dataflowName, err)
		}
		err = c.orch.AwaitDataflowReady(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed to receive completion for dataflow '%s': %w", dataflowName, err)
		}
		c.logger.Info().Str("dataflow", dataflowName).Msg("Resource setup is complete.")
	}
	c.logger.Info().Msg("Step 3: Dataflow resource setup is complete.")
	return nil
}

// applyDataflowIAM is phase 4 of the orchestration.
func (c *Conductor) applyDataflowIAM(ctx context.Context) error {
	if !c.options.ApplyDataflowIAM {
		c.logger.Info().Msg("Step 4: Skipping application service IAM setup.")
		return nil
	}

	c.logger.Info().Msg("Executing Step 4: Applying application service IAM...")
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
	c.logger.Info().Msg("Step 4: Application service IAM setup is complete.")
	return nil
}

// deployDataflowServices is phase 5 of the orchestration.
func (c *Conductor) deployDataflowServices(ctx context.Context) error {
	if !c.options.DeployDataflowServices {
		c.logger.Info().Msg("Step 5: Skipping deployment of dataflow services.")
		return nil
	}

	c.logger.Info().Msg("Executing Step 5: Deploying dataflow services...")
	// This step depends on the director URL from a previous step.
	if c.directorURL == "" {
		return errors.New("cannot deploy dataflow services: director URL is not available. Was the director deployed or a URL override provided?")
	}

	for dataflowName := range c.arch.Dataflows {
		err := c.orch.DeployDataflowServices(ctx, dataflowName, c.serviceAccountEmails, c.directorURL)
		if err != nil {
			return fmt.Errorf("failed to deploy application services for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Step 5: Deployment of all dataflow services is complete.")
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
