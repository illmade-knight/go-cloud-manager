package iam

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMManager defines the high-level orchestration logic for applying IAM policies.
// It is the central component for transforming an architecture into applied cloud permissions.
type IAMManager interface {
	// ApplyIAMForDataflow plans and applies all necessary IAM roles for a dataflow,
	// returning a map of the final policies that were applied for verification.
	ApplyIAMForDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) (map[string]PolicyBinding, error)
}

// simpleIAMManager is the concrete implementation of the IAMManager interface.
type simpleIAMManager struct {
	client  IAMClient
	logger  zerolog.Logger
	planner *RolePlanner
}

// NewIAMManager creates a new IAMManager.
func NewIAMManager(client IAMClient, logger zerolog.Logger) (IAMManager, error) {
	if client == nil {
		return nil, fmt.Errorf("iam client cannot be nil")
	}
	return &simpleIAMManager{
		client:  client,
		logger:  logger.With().Str("component", "IAMManager").Logger(),
		planner: NewRolePlanner(logger),
	}, nil
}

// ApplyIAMForDataflow is the core orchestration method of the manager.
// REFACTOR: This function now correctly returns the map of applied policies.
func (im *simpleIAMManager) ApplyIAMForDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) (map[string]PolicyBinding, error) {
	im.logger.Info().Str("dataflow", dataflowName).Msg("Planning and applying IAM for all services in dataflow.")

	dataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return nil, fmt.Errorf("dataflow '%s' not found in architecture", dataflowName)
	}
	saLogicalNameToEmail := make(map[string]string)
	for _, serviceSpec := range dataflow.Services {
		saEmail, err := im.client.EnsureServiceAccountExists(ctx, serviceSpec.ServiceAccount)
		if err != nil {
			return nil, fmt.Errorf("failed to ensure SA '%s' for service '%s': %w", serviceSpec.ServiceAccount, serviceSpec.Name, err)
		}
		saLogicalNameToEmail[serviceSpec.ServiceAccount] = saEmail
	}

	iamPlan, err := im.planner.PlanRolesForApplicationServices(arch)
	if err != nil {
		return nil, fmt.Errorf("failed to generate IAM plan for dataflow '%s': %w", dataflowName, err)
	}

	resourcePolicies, err := im.assembleResourcePolicies(iamPlan, saLogicalNameToEmail)
	if err != nil {
		return nil, err
	}

	im.logger.Info().Int("resources_with_policies", len(resourcePolicies)).Msg("Applying policies atomically per resource...")
	for _, policyBinding := range resourcePolicies {
		if err := im.client.ApplyIAMPolicy(ctx, policyBinding); err != nil {
			return nil, fmt.Errorf("failed to apply policy for resource '%s' ('%s'): %w", policyBinding.ResourceID, policyBinding.ResourceType, err)
		}
	}

	im.logger.Info().Str("dataflow", dataflowName).Msg("Successfully applied IAM for all services in dataflow.")
	return resourcePolicies, nil
}

// assembleResourcePolicies contains the clean, testable logic for transforming a flat plan
// into a map of resource-grouped policies, ready for atomic application.
func (im *simpleIAMManager) assembleResourcePolicies(
	iamPlan []IAMBinding,
	saLogicalNameToEmail map[string]string,
) (map[string]PolicyBinding, error) {

	type resourceKey struct{ Type, ID, Location string }
	policyMap := make(map[resourceKey]PolicyBinding)

	for _, binding := range iamPlan {
		memberEmail, ok := saLogicalNameToEmail[binding.ServiceAccount]
		if !ok {
			continue
		}
		member := "serviceAccount:" + memberEmail
		key := resourceKey{Type: binding.ResourceType, ID: binding.ResourceID, Location: binding.ResourceLocation}

		if _, ok := policyMap[key]; !ok {
			policyMap[key] = PolicyBinding{
				ResourceType:     binding.ResourceType,
				ResourceID:       binding.ResourceID,
				ResourceLocation: binding.ResourceLocation,
				MemberRoles:      make(map[string][]string),
			}
		}
		policy := policyMap[key]
		policy.MemberRoles[binding.Role] = append(policy.MemberRoles[binding.Role], member)
		policyMap[key] = policy
	}

	finalPolicies := make(map[string]PolicyBinding)
	for key, policy := range policyMap {
		finalKey := fmt.Sprintf("%s:%s", key.Type, key.ID)
		finalPolicies[finalKey] = policy
	}

	return finalPolicies, nil
}
