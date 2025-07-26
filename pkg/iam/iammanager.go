package iam

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMManager defines the high-level orchestration logic for applying IAM policies.
// The interface remains unchanged, preventing cascading refactors.
type IAMManager interface {
	ApplyIAMForService(ctx context.Context, dataflow servicemanager.ResourceGroup, serviceName string) error
}

type simpleIAMManager struct {
	client  IAMClient
	logger  zerolog.Logger
	planner *RolePlanner
	// The manager now holds the full architecture and the complete IAM plan.
	architecture *servicemanager.MicroserviceArchitecture
	iamPlan      map[string][]IAMBinding
}

// NewIAMManager now accepts the full architecture at creation time.
// It immediately plans all roles, making the Apply step a simple lookup.
func NewIAMManager(client IAMClient, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (IAMManager, error) {
	planner := NewRolePlanner(logger)
	// The plan for the entire architecture is generated once, right at the start.
	fullPlan, err := planner.PlanRolesForApplicationServices(arch)
	if err != nil {
		return nil, fmt.Errorf("failed to generate IAM plan during manager initialization: %w", err)
	}

	return &simpleIAMManager{
		client:       client,
		logger:       logger.With().Str("component", "IAMManager").Logger(),
		planner:      planner,
		architecture: arch,
		iamPlan:      fullPlan,
	}, nil
}

// ApplyIAMForService's signature is unchanged. It now uses its internal plan.
func (im *simpleIAMManager) ApplyIAMForService(ctx context.Context, dataflow servicemanager.ResourceGroup, serviceName string) error {
	im.logger.Info().Str("service", serviceName).Str("dataflow", dataflow.Name).Msg("Applying IAM policies...")

	serviceSpec, ok := dataflow.Services[serviceName]
	if !ok {
		return fmt.Errorf("service '%s' not found in dataflow '%s'", serviceName, dataflow.Name)
	}
	serviceAccountName := serviceSpec.ServiceAccount

	// Look up the pre-computed bindings for this service account from the master plan.
	bindings, ok := im.iamPlan[serviceAccountName]
	if !ok {
		im.logger.Info().Str("service_account", serviceAccountName).Msg("No IAM bindings were planned for this service. Nothing to apply.")
		return nil
	}

	saEmail, err := im.client.EnsureServiceAccountExists(ctx, serviceAccountName)
	if err != nil {
		return fmt.Errorf("failed to ensure service account '%s' exists: %w", serviceAccountName, err)
	}

	member := "serviceAccount:" + saEmail

	// Execute the plan for this service.
	for _, binding := range bindings {
		im.logger.Info().
			Str("resource", binding.ResourceID).
			Str("role", binding.Role).
			Msgf("Applying binding for service '%s'", serviceName)

		if err := im.client.AddResourceIAMBinding(ctx, binding, member); err != nil {
			return fmt.Errorf("failed to apply binding for resource '%s' with role '%s': %w", binding.ResourceID, binding.Role, err)
		}
	}

	im.logger.Info().Str("service", serviceName).Int("bindings_applied", len(bindings)).Msg("Successfully applied all planned IAM policies.")
	return nil
}
