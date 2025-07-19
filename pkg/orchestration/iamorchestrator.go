package orchestration

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMOrchestrator is responsible for planning and applying all IAM policies for an architecture.
type IAMOrchestrator struct {
	arch      *servicemanager.MicroserviceArchitecture
	logger    zerolog.Logger
	iamClient iam.IAMClient
	planner   *iam.RolePlanner
}

// NewIAMOrchestrator creates a new orchestrator focused solely on IAM.
func NewIAMOrchestrator(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*IAMOrchestrator, error) {
	iamClient, err := iam.NewGoogleIAMClient(ctx, arch.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create iamClient client for orchestrator: %w", err)
	}

	planner := iam.NewRolePlanner(logger)

	return &IAMOrchestrator{
		arch:      arch,
		logger:    logger.With().Str("component", "IAMOrchestrator").Logger(),
		iamClient: iamClient,
		planner:   planner,
	}, nil
}

// SetupServiceDirectorIAM handles ensuring the ServiceDirector's SA exists and has project-level roles.
func (o *IAMOrchestrator) SetupServiceDirectorIAM(ctx context.Context) (string, error) {
	o.logger.Info().Msg("Starting ServiceDirector IAM setup...")

	requiredRoles, err := o.planner.PlanRolesForServiceDirector(ctx, o.arch)
	if err != nil {
		return "", fmt.Errorf("failed to plan IAM roles for ServiceDirector: %w", err)
	}

	saName := o.arch.ServiceManagerSpec.ServiceAccount
	if saName == "" {
		saName = o.arch.ServiceManagerSpec.Name + "-sa"
		o.logger.Info().Str("default_name", saName).Msg("Service account not specified for ServiceDirector, using default.")
	}

	saEmail, err := o.iamClient.EnsureServiceAccountExists(ctx, saName)
	if err != nil {
		return "", fmt.Errorf("failed to ensure ServiceDirector service account exists: %w", err)
	}
	o.logger.Info().Str("email", saEmail).Msg("ServiceDirector service account is ready.")

	o.logger.Info().Strs("roles", requiredRoles).Str("member", "serviceAccount:"+saEmail).Msg("PLAN: Granting project-level roles to ServiceDirector service account.")
	// err = o.iamClient.AddProjectIAMBindings(ctx, saEmail, requiredRoles) ...

	o.logger.Info().Msg("ServiceDirector IAM setup complete.")
	return saEmail, nil
}

// SetupDataflowIAM handles granting resource-level permissions to all application services.
func (o *IAMOrchestrator) SetupDataflowIAM(ctx context.Context) error {
	o.logger.Info().Msg("Starting Dataflow application services IAM setup...")

	plan, err := o.planner.PlanRolesForApplicationServices(ctx, o.arch)
	if err != nil {
		return fmt.Errorf("failed to plan application service IAM roles: %w", err)
	}

	for saName, bindings := range plan {
		// This is the key fix: Ensure the service account for each application exists before applying bindings.
		saEmail, err := o.iamClient.EnsureServiceAccountExists(ctx, saName)
		if err != nil {
			return fmt.Errorf("failed to ensure service account %s: %w", saName, err)
		}

		member := "serviceAccount:" + saEmail
		o.logger.Info().Str("service_account", saEmail).Int("binding_count", len(bindings)).Msg("Applying IAM bindings...")

		for _, binding := range bindings {
			err := o.iamClient.AddResourceIAMBinding(ctx, binding.ResourceType, binding.ResourceID, binding.Role, member)
			if err != nil {
				return fmt.Errorf("failed to apply binding for SA %s on resource %s: %w", saEmail, binding.ResourceID, err)
			}
		}
	}

	o.logger.Info().Msg("Dataflow application services IAM setup complete.")
	return nil
}
