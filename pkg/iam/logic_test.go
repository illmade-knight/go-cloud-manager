package iam_test

import (
	"fmt"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// assembleResourcePolicies is the standalone function we are testing.
// Its single responsibility is to take an architecture and a dataflow name,
// plan all the necessary IAM roles, and then group them into a map where each
// key represents a unique cloud resource and the value is the complete, final
// IAM policy to be applied to that resource.
func assembleResourcePolicies(
	arch *servicemanager.MicroserviceArchitecture,
	dataflowName string,
	saLogicalNameToEmail map[string]string,
) (map[string]iam.PolicyBinding, error) {

	planner := iam.NewRolePlanner(zerolog.Nop())

	// 1. The Planner's Job: Understand the *meaning* of the architecture.
	iamPlan, err := planner.PlanRolesForApplicationServices(arch)
	if err != nil {
		return nil, fmt.Errorf("failed to generate IAM plan: %w", err)
	}

	// 2. The Assembler's Job: Prepare the plan for *execution*.
	type resourceKey struct{ Type, ID, Location string }
	resourcePolicies := make(map[resourceKey]iam.PolicyBinding)

	for _, binding := range iamPlan {
		memberEmail, ok := saLogicalNameToEmail[binding.ServiceAccount]
		if !ok {
			continue
		}
		member := "serviceAccount:" + memberEmail
		key := resourceKey{Type: binding.ResourceType, ID: binding.ResourceID, Location: binding.ResourceLocation}

		if _, ok := resourcePolicies[key]; !ok {
			resourcePolicies[key] = iam.PolicyBinding{
				ResourceType:     binding.ResourceType,
				ResourceID:       binding.ResourceID,
				ResourceLocation: binding.ResourceLocation,
				MemberRoles:      make(map[string][]string),
			}
		}
		policy := resourcePolicies[key]
		policy.MemberRoles[binding.Role] = append(policy.MemberRoles[binding.Role], member)
		resourcePolicies[key] = policy
	}

	finalPolicies := make(map[string]iam.PolicyBinding)
	for key, policy := range resourcePolicies {
		finalKey := fmt.Sprintf("%s:%s", key.Type, key.ID)
		finalPolicies[finalKey] = policy
	}

	return finalPolicies, nil
}

// TestAssembleResourcePolicies verifies that the logic for transforming an architecture
// into a resource-centric IAM policy map is correct.
func TestAssembleResourcePolicies(t *testing.T) {
	// --- Common Test Data ---
	baseArch := servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: "test-project", Region: "us-central1"},
	}

	// --- Test Cases ---
	testCases := []struct {
		name                 string
		arch                 *servicemanager.MicroserviceArchitecture
		dataflowName         string
		saMap                map[string]string
		expectedResourceKeys []string
		expectedPolicies     map[string]iam.PolicyBinding
	}{
		{
			name: "Architecture from orchestrator_dataflow_test",
			arch: &servicemanager.MicroserviceArchitecture{
				Environment: baseArch.Environment,
				Dataflows: map[string]servicemanager.ResourceGroup{
					"tracer-flow": {
						Services: map[string]servicemanager.ServiceSpec{
							"tracer-subscriber": {ServiceAccount: "sub-sa"},
							"tracer-publisher":  {ServiceAccount: "pub-sa"},
						},
						Resources: servicemanager.CloudResourcesSpec{
							Topics: []servicemanager.TopicConfig{
								{CloudResource: servicemanager.CloudResource{Name: "tracer-topic"}, ProducerService: &servicemanager.ServiceMapping{Name: "tracer-publisher"}},
								{CloudResource: servicemanager.CloudResource{Name: "verify-topic"}, ProducerService: &servicemanager.ServiceMapping{Name: "tracer-subscriber"}},
							},
							Subscriptions: []servicemanager.SubscriptionConfig{{
								CloudResource:   servicemanager.CloudResource{Name: "tracer-sub"},
								Topic:           "tracer-topic",
								ConsumerService: &servicemanager.ServiceMapping{Name: "tracer-subscriber"},
							}},
						},
					},
				},
			},
			dataflowName: "tracer-flow",
			saMap: map[string]string{
				"sub-sa": "subscriber-email@project.iam.gserviceaccount.com",
				"pub-sa": "publisher-email@project.iam.gserviceaccount.com",
			},
			expectedResourceKeys: []string{
				"pubsub_topic:tracer-topic",
				"pubsub_topic:verify-topic",
				"pubsub_subscription:tracer-sub",
			},
			expectedPolicies: map[string]iam.PolicyBinding{
				"pubsub_topic:tracer-topic": {
					ResourceType: "pubsub_topic", ResourceID: "tracer-topic",
					MemberRoles: map[string][]string{
						"roles/pubsub.publisher": {"serviceAccount:publisher-email@project.iam.gserviceaccount.com"},
						"roles/pubsub.viewer":    {"serviceAccount:publisher-email@project.iam.gserviceaccount.com", "serviceAccount:subscriber-email@project.iam.gserviceaccount.com"},
					},
				},
				"pubsub_topic:verify-topic": {
					ResourceType: "pubsub_topic", ResourceID: "verify-topic",
					MemberRoles: map[string][]string{
						"roles/pubsub.publisher": {"serviceAccount:subscriber-email@project.iam.gserviceaccount.com"},
						"roles/pubsub.viewer":    {"serviceAccount:subscriber-email@project.iam.gserviceaccount.com"},
					},
				},
				"pubsub_subscription:tracer-sub": {
					ResourceType: "pubsub_subscription", ResourceID: "tracer-sub",
					MemberRoles: map[string][]string{
						"roles/pubsub.subscriber": {"serviceAccount:subscriber-email@project.iam.gserviceaccount.com"},
						"roles/pubsub.viewer":     {"serviceAccount:subscriber-email@project.iam.gserviceaccount.com"},
					},
				},
			},
		},
		{
			name: "Service with no planned IAM bindings",
			arch: &servicemanager.MicroserviceArchitecture{
				Environment: baseArch.Environment,
				Dataflows: map[string]servicemanager.ResourceGroup{
					"simple-flow": {
						Services: map[string]servicemanager.ServiceSpec{
							"lonely-service": {ServiceAccount: "lonely-sa"},
						},
					},
				},
			},
			dataflowName:         "simple-flow",
			saMap:                map[string]string{"lonely-sa": "lonely-email@project.iam.gserviceaccount.com"},
			expectedResourceKeys: []string{}, // Expect no policies to be generated.
			expectedPolicies:     map[string]iam.PolicyBinding{},
		},
		{
			name: "Dataflow with no services",
			arch: &servicemanager.MicroserviceArchitecture{
				Environment: baseArch.Environment,
				Dataflows: map[string]servicemanager.ResourceGroup{
					"empty-flow": {
						Services: map[string]servicemanager.ServiceSpec{},
						Resources: servicemanager.CloudResourcesSpec{
							Topics: []servicemanager.TopicConfig{{CloudResource: servicemanager.CloudResource{Name: "unused-topic"}}},
						},
					},
				},
			},
			dataflowName:         "empty-flow",
			saMap:                map[string]string{},
			expectedResourceKeys: []string{}, // Expect no policies as there are no services to grant them to.
			expectedPolicies:     map[string]iam.PolicyBinding{},
		},
	}

	// --- Run Test Cases ---
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// ACT
			actualPolicies, err := assembleResourcePolicies(tc.arch, tc.dataflowName, tc.saMap)

			// ASSERT
			require.NoError(t, err)
			require.NotNil(t, actualPolicies)
			require.Len(t, actualPolicies, len(tc.expectedResourceKeys))

			for _, key := range tc.expectedResourceKeys {
				t.Logf("Verifying policy for resource: %s", key)
				expectedPolicy, ok := tc.expectedPolicies[key]
				require.True(t, ok, "Expected policy for resource %s was not found in expectations", key)
				actualPolicy, ok := actualPolicies[key]
				require.True(t, ok, "Actual policy for resource %s was not found in results", key)
				require.Equal(t, expectedPolicy.ResourceType, actualPolicy.ResourceType)
				require.Equal(t, expectedPolicy.ResourceID, actualPolicy.ResourceID)
				require.Len(t, actualPolicy.MemberRoles, len(expectedPolicy.MemberRoles))
				for role, expectedMembers := range expectedPolicy.MemberRoles {
					actualMembers, ok := actualPolicy.MemberRoles[role]
					require.True(t, ok, "Expected role %s not found in actual policy for resource %s", role, key)
					require.ElementsMatch(t, expectedMembers, actualMembers, "Member list for role %s did not match", role)
				}
			}
		})
	}
}
