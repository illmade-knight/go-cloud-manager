package orchestration

import (
	"context"
	"fmt"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// Conductor manages the high-level orchestration of a full microservice architecture deployment.
type Conductor struct {
	arch    *servicemanager.MicroserviceArchitecture
	logger  zerolog.Logger
	iamOrch *IAMOrchestrator
	orch    *Orchestrator
}

// NewConductor creates and initializes a new Conductor.
func NewConductor(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*Conductor, error) {
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
	}, nil
}

// Run executes the entire orchestration workflow in the correct sequence.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration...")

	// 1. Setup IAM for the ServiceDirector ONLY.
	sdSaEmails, err := c.iamOrch.SetupServiceDirectorIAM(ctx)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector IAM setup: %w", err)
	}

	// 2. Deploy the ServiceDirector and wait for it to report ready.
	directorURL, err := c.orch.DeployServiceDirector(ctx, sdSaEmails)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector deployment: %w", err)
	}
	if err := c.orch.AwaitServiceReady(ctx, c.arch.ServiceManagerSpec.Name); err != nil {
		return fmt.Errorf("failed while waiting for ServiceDirector to become ready: %w", err)
	}
	c.logger.Info().Msg("ServiceDirector is deployed and ready.")

	// 3. Trigger and await resource creation for all dataflows.
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

	// 4. NOW that resources exist, set up IAM for the individual application services.
	dfSaEmails, err := c.iamOrch.SetupDataflowIAM(ctx)
	if err != nil {
		return fmt.Errorf("failed during application service IAM setup: %w", err)
	}

	// Combine all service account maps for the final deployment step.
	allSaEmails := make(map[string]string)
	for k, v := range sdSaEmails {
		allSaEmails[k] = v
	}
	for k, v := range dfSaEmails {
		allSaEmails[k] = v
	}

	// 5. Deploy all the application services for all dataflows.
	for dataflowName := range c.arch.Dataflows {
		if err := c.orch.DeployDataflowServices(ctx, dataflowName, allSaEmails, directorURL); err != nil {
			return fmt.Errorf("failed to deploy application services for dataflow '%s': %w", dataflowName, err)
		}
	}

	c.logger.Info().Msg("✅ Full architecture orchestration completed successfully.")
	return nil
}

// Teardown cleans up all resources managed by the conductor.
func (c *Conductor) Teardown(ctx context.Context) error {
	c.logger.Info().Msg("--- Starting Conductor Teardown ---")
	var errs []error

	// Teardown is LIFO: first the main orchestrator's resources, then IAM.
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
