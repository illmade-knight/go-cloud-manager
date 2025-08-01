package iam

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMManager defines the high-level orchestration logic for applying IAM policies.
type IAMManager interface {
	// ApplyIAMForService plans and applies all necessary IAM roles for a single service
	// within a given dataflow.
	ApplyIAMForService(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string, serviceName string) error
}

// simpleIAMManager is the concrete implementation of the IAMManager interface.
type simpleIAMManager struct {
	client  IAMClient
	logger  zerolog.Logger
	planner *RolePlanner
}

// NewIAMManager creates a new IAMManager.
// It is a lightweight constructor that initializes the manager with its dependencies.
func NewIAMManager(client IAMClient, logger zerolog.Logger) (IAMManager, error) {
	return &simpleIAMManager{
		client:  client,
		logger:  logger.With().Str("component", "IAMManager").Logger(),
		planner: NewRolePlanner(logger),
	}, nil
}

// ApplyIAMForService plans and applies all necessary IAM roles for a single service.
// This is the core orchestration method of the manager. It performs the following steps:
// 1. Looks up the specified dataflow and service from the architecture.
// 2. Invokes the RolePlanner to generate a complete IAM plan for all application services.
// 3. Filters the plan to get the specific bindings required for the target service's service account.
// 4. Ensures the service account exists, creating it if necessary.
// 5. Iterates through the planned bindings and applies each one using the IAMClient.
func (im *simpleIAMManager) ApplyIAMForService(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string, serviceName string) error {
	// Look up the dataflow object from the architecture using the provided name.
	dataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow '%s' not found in architecture", dataflowName)
	}

	im.logger.Info().Str("service", serviceName).Str("dataflow", dataflow.Name).Msg("Planning and applying IAM policies...")

	// 1. Plan roles for all services just-in-time, ensuring the plan is based on the latest architecture state.
	iamPlan, err := im.planner.PlanRolesForApplicationServices(arch)
	if err != nil {
		return fmt.Errorf("failed to generate IAM plan for service '%s': %w", serviceName, err)
	}

	serviceSpec, ok := dataflow.Services[serviceName]
	if !ok {
		return fmt.Errorf("service '%s' not found in dataflow '%s'", serviceName, dataflow.Name)
	}
	serviceAccountName := serviceSpec.ServiceAccount

	// 2. Look up the specific bindings for this service's service account from the generated plan.
	bindings, ok := iamPlan[serviceAccountName]
	if !ok {
		im.logger.Info().Str("service_account", serviceAccountName).Msg("No IAM bindings were planned for this service. Nothing to apply.")
		return nil
	}

	// 3. Ensure the service account exists before trying to grant it permissions.
	saEmail, err := im.client.EnsureServiceAccountExists(ctx, serviceAccountName)
	if err != nil {
		return fmt.Errorf("failed to ensure service account '%s' exists: %w", serviceAccountName, err)
	}

	member := "serviceAccount:" + saEmail

	// 4. Execute the plan by applying each binding.
	for _, binding := range bindings {
		im.logger.Info().
			Str("member", member).
			Str("role", binding.Role).
			Str("resource_type", binding.ResourceType).
			Str("resource_id", binding.ResourceID).
			Msgf("Applying binding for service '%s'", serviceName)

		err = im.client.AddResourceIAMBinding(ctx, binding, member)
		if err != nil {
			return fmt.Errorf("failed to apply binding for resource '%s' with role '%s' to member '%s': %w", binding.ResourceID, binding.Role, member, err)
		}
	}

	im.logger.Info().Str("service", serviceName).Int("bindings_applied", len(bindings)).Msg("Successfully applied all planned IAM policies.")
	return nil
}
