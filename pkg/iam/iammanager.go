package iam

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMManager defines the high-level orchestration logic for applying IAM policies.
type IAMManager interface {
	ApplyIAMForService(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string, serviceName string) error
}

type simpleIAMManager struct {
	client  IAMClient
	logger  zerolog.Logger
	planner *RolePlanner
}

// NewIAMManager creates a new IAMManager.
// It is now simpler and no longer creates a full IAM plan upon initialization.
func NewIAMManager(client IAMClient, logger zerolog.Logger) (IAMManager, error) {
	return &simpleIAMManager{
		client:  client,
		logger:  logger.With().Str("component", "IAMManager").Logger(),
		planner: NewRolePlanner(logger),
	}, nil
}

// ApplyIAMForService plans and applies all necessary IAM roles for a single service.
// It now looks up the dataflow by name and runs the planner just-in-time.
func (im *simpleIAMManager) ApplyIAMForService(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string, serviceName string) error {
	// Lookup the dataflow object from the architecture using the name.
	dataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow '%s' not found in architecture", dataflowName)
	}

	im.logger.Info().Str("service", serviceName).Str("dataflow", dataflow.Name).Msg("Planning and applying IAM policies...")

	// 1. Plan roles for all services now that resources are expected to exist.
	iamPlan, err := im.planner.PlanRolesForApplicationServices(arch)
	if err != nil {
		return fmt.Errorf("failed to generate IAM plan for service '%s': %w", serviceName, err)
	}

	serviceSpec, ok := dataflow.Services[serviceName]
	if !ok {
		return fmt.Errorf("service '%s' not found in dataflow '%s'", serviceName, dataflow.Name)
	}
	serviceAccountName := serviceSpec.ServiceAccount

	// 2. Look up the bindings from the plan we just created.
	bindings, ok := iamPlan[serviceAccountName]
	if !ok {
		im.logger.Info().Str("service_account", serviceAccountName).Msg("No IAM bindings were planned for this service. Nothing to apply.")
		return nil
	}

	saEmail, err := im.client.EnsureServiceAccountExists(ctx, serviceAccountName)
	if err != nil {
		return fmt.Errorf("failed to ensure service account '%s' exists: %w", serviceAccountName, err)
	}

	member := "serviceAccount:" + saEmail

	// 3. Execute the plan.
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
