package orchestration

import (
	"context"
	"fmt"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// Conductor manages the high-level orchestration of a full microservice architecture deployment.
type Conductor struct {
	arch       *servicemanager.MicroserviceArchitecture
	logger     zerolog.Logger
	iamOrch    *IAMOrchestrator
	orch       *Orchestrator
	runID      string
	sourcePath string
}

// NewConductor creates and initializes a new Conductor.
func NewConductor(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, sourcePath, runID string, logger zerolog.Logger) (*Conductor, error) {
	iamOrch, err := NewIAMOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM orchestrator: %w", err)
	}

	orch, err := NewOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create main orchestrator: %w", err)
	}

	return &Conductor{
		arch:       arch,
		logger:     logger.With().Str("component", "Conductor").Logger(),
		iamOrch:    iamOrch,
		orch:       orch,
		runID:      runID,
		sourcePath: sourcePath,
	}, nil
}

// Run executes the entire orchestration workflow in the correct sequence.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration...")

	// 1. Setup all IAM for the ServiceDirector first.
	saEmail, err := c.iamOrch.SetupServiceDirectorIAM(ctx)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector IAM setup: %w", err)
	}

	// 2. Deploy the ServiceDirector.
	directorURL, err := c.orch.DeployServiceDirector(ctx, saEmail)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector deployment: %w", err)
	}

	// 3. Trigger and await resource creation for all dataflows.
	for dataflowName := range c.arch.Dataflows {
		c.logger.Info().Str("dataflow", dataflowName).Msg("Triggering setup for dataflow...")
		if err := c.orch.TriggerDataflowSetup(ctx, dataflowName); err != nil {
			return fmt.Errorf("failed to trigger setup for dataflow '%s': %w", dataflowName, err)
		}
		if err := c.orch.AwaitDataflowReady(ctx, dataflowName); err != nil {
			return fmt.Errorf("failed to receive completion for dataflow '%s': %w", dataflowName, err)
		}
		c.logger.Info().Str("dataflow", dataflowName).Msg("Dataflow setup complete.")
	}

	// 4. Setup all IAM for the individual application services.
	if err := c.iamOrch.SetupDataflowIAM(ctx); err != nil {
		return fmt.Errorf("failed during application service IAM setup: %w", err)
	}

	// 5. Deploy all the application services for all dataflows.
	for dataflowName := range c.arch.Dataflows {
		if err := c.orch.DeployDataflowServices(ctx, dataflowName, directorURL); err != nil {
			return fmt.Errorf("failed to deploy application services for dataflow '%s': %w", dataflowName, err)
		}
	}

	c.logger.Info().Msg("✅ Full architecture orchestration completed successfully.")
	return nil
}
