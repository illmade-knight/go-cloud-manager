package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// ConductorOptions provides flags to control the orchestration workflow.
type ConductorOptions struct {
	// Step Flags: Set to true to execute the step, false to skip.
	SetupServiceDirectorIAM bool
	DeployServiceDirector   bool
	SetupDataflowResources  bool
	ApplyDataflowIAM        bool
	DeployDataflowServices  bool

	// DirectorURLOverride is required if DeployServiceDirector is false but DeployDataflowServices is true.
	DirectorURLOverride string
}

// Conductor manages the high-level orchestration of a full microservice architecture deployment.
type Conductor struct {
	arch    *servicemanager.MicroserviceArchitecture
	logger  zerolog.Logger
	iamOrch *IAMOrchestrator
	orch    *Orchestrator
	options ConductorOptions
}

// NewConductor creates and initializes a new Conductor with specific options.
func NewConductor(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, opts ConductorOptions) (*Conductor, error) {
	iamOrch, err := NewIAMOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM orchestrator: %w", err)
	}

	orch, err := NewOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create main orchestrator: %w", err)
	}

	return &Conductor{
		arch:    arch,
		logger:  logger.With().Str("component", "Conductor").Logger(),
		iamOrch: iamOrch,
		orch:    orch,
		options: opts,
	}, nil
}

// Run executes the orchestration workflow according to the configured options.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration with specified options...")

	var sdSaEmails map[string]string
	var directorURL string
	var err error

	// 1. Setup IAM for the ServiceDirector ONLY.
	if c.options.SetupServiceDirectorIAM {
		sdSaEmails, err = c.iamOrch.SetupServiceDirectorIAM(ctx)
		if err != nil {
			return fmt.Errorf("failed during ServiceDirector IAM setup: %w", err)
		}
		c.logger.Info().Msg("Step 1: ServiceDirector IAM setup complete.")
	} else {
		c.logger.Info().Msg("Step 1: Skipping ServiceDirector IAM setup.")
	}

	// 2. Deploy the ServiceDirector and wait for it to report ready.
	if c.options.DeployServiceDirector {
		directorURL, err = c.orch.DeployServiceDirector(ctx, sdSaEmails)
		if err != nil {
			return fmt.Errorf("failed during ServiceDirector deployment: %w", err)
		}
		if err := c.orch.AwaitServiceReady(ctx, c.arch.ServiceManagerSpec.Name); err != nil {
			return fmt.Errorf("failed while waiting for ServiceDirector to become ready: %w", err)
		}
		c.logger.Info().Str("url", directorURL).Msg("Step 2: ServiceDirector is deployed and ready.")
	} else {
		c.logger.Info().Msg("Step 2: Skipping ServiceDirector deployment.")
		// If we skip deployment but need the URL later, we must use the override.
		if c.options.DeployDataflowServices {
			if c.options.DirectorURLOverride == "" {
				return errors.New("DeployServiceDirector was skipped, but DeployDataflowServices requires a DirectorURLOverride")
			}
			directorURL = c.options.DirectorURLOverride
			c.logger.Info().Str("url", directorURL).Msg("Using provided Director URL override.")
		}
	}

	// 3. Trigger and await resource creation for all dataflows.
	if c.options.SetupDataflowResources {
		for dataflowName := range c.arch.Dataflows {
			c.logger.Info().Str("dataflow", dataflowName).Msg("Triggering resource setup...")
			if err := c.orch.TriggerDataflowSetup(ctx, dataflowName); err != nil {
				return fmt.Errorf("failed to trigger setup for dataflow '%s': %w", dataflowName, err)
			}
			if err := c.orch.AwaitDataflowReady(ctx, dataflowName); err != nil {
				return fmt.Errorf("failed to receive completion for dataflow '%s': %w", dataflowName, err)
			}
			c.logger.Info().Str("dataflow", dataflowName).Msg("Resource setup is complete.")
		}
		c.logger.Info().Msg("Step 3: Dataflow resource setup is complete.")
	} else {
		c.logger.Info().Msg("Step 3: Skipping dataflow resource setup.")
	}

	// This map is needed for the final deployment step.
	allSaEmails := make(map[string]string)

	// 4. Set up IAM for the application services and collect their SA emails.
	if c.options.ApplyDataflowIAM {
		allDataflowSaEmails := make(map[string]string)
		for dataflowName := range c.arch.Dataflows {
			dfSaEmails, err := c.iamOrch.ApplyIAMForDataflow(ctx, dataflowName)
			if err != nil {
				return fmt.Errorf("failed during application service IAM setup for dataflow '%s': %w", dataflowName, err)
			}
			for k, v := range dfSaEmails {
				allDataflowSaEmails[k] = v
			}
		}

		// Combine all service account maps for the final deployment step.
		for k, v := range sdSaEmails {
			allSaEmails[k] = v
		}
		for k, v := range allDataflowSaEmails {
			allSaEmails[k] = v
		}
		c.logger.Info().Msg("Step 4: Application service IAM setup is complete.")
	} else {
		c.logger.Info().Msg("Step 4: Skipping application service IAM setup.")
	}

	// 5. Deploy all the application services for all dataflows.
	if c.options.DeployDataflowServices {
		for dataflowName := range c.arch.Dataflows {
			if err := c.orch.DeployDataflowServices(ctx, dataflowName, allSaEmails, directorURL); err != nil {
				return fmt.Errorf("failed to deploy application services for dataflow '%s': %w", dataflowName, err)
			}
		}
		c.logger.Info().Msg("Step 5: Deployment of all dataflow services is complete.")
	} else {
		c.logger.Info().Msg("Step 5: Skipping deployment of dataflow services.")
	}

	c.logger.Info().Msg("✅ Full architecture orchestration completed successfully.")
	return nil
}

// Teardown cleans up all resources managed by the conductor.
func (c *Conductor) Teardown(ctx context.Context) error {
	c.logger.Info().Msg("--- Starting Conductor Teardown ---")
	var errs []error

	if err := c.orch.Teardown(ctx); err != nil {
		c.logger.Error().Err(err).Msg("Orchestrator teardown failed")
		errs = append(errs, err)
	}
	if err := c.iamOrch.Teardown(ctx); err != nil {
		c.logger.Error().Err(err).Msg("IAM teardown failed")
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("conductor teardown failed with %d errors", len(errs))
	}
	c.logger.Info().Msg("--- Conductor Teardown Complete ---")
	return nil
}
