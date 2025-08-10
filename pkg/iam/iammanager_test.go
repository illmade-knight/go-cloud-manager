package iam_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// mockIAMClient is a "spy" that records calls to ApplyIAMPolicy for verification.
type mockIAMClient struct {
	mu              sync.Mutex
	AppliedPolicies []iam.PolicyBinding
}

func (m *mockIAMClient) ApplyIAMPolicy(ctx context.Context, binding iam.PolicyBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AppliedPolicies = append(m.AppliedPolicies, binding)
	return nil
}
func (m *mockIAMClient) EnsureServiceAccountExists(_ context.Context, accountName string) (string, error) {
	return "mock-" + accountName + "@project.iam.gserviceaccount.com", nil
}
func (m *mockIAMClient) GetServiceAccount(ctx context.Context, accountEmail string) error { return nil }
func (m *mockIAMClient) AddResourceIAMBinding(_ context.Context, _ iam.IAMBinding, _ string) error {
	return nil
}
func (m *mockIAMClient) RemoveResourceIAMBinding(_ context.Context, _ iam.IAMBinding, _ string) error {
	return nil
}
func (m *mockIAMClient) CheckResourceIAMBinding(_ context.Context, _ iam.IAMBinding, _ string) (bool, error) {
	return true, nil
}
func (m *mockIAMClient) AddArtifactRegistryRepositoryIAMBinding(_ context.Context, _, _, _, _ string) error {
	return nil
}
func (m *mockIAMClient) DeleteServiceAccount(ctx context.Context, accountName string) error {
	return nil
}
func (m *mockIAMClient) AddMemberToServiceAccountRole(ctx context.Context, a, b, c string) error {
	return nil
}
func (m *mockIAMClient) Close() error { return nil }

// TestIAMManager_ApplyIAMForDataflow is a table-driven test that verifies the manager's
// core logic of planning, assembling, and applying IAM policies atomically.
func TestIAMManager_ApplyIAMForDataflow(t *testing.T) {
	// --- Common Test Data ---
	baseArch := servicemanager.MicroserviceArchitecture{
		Environment: servicemanager.Environment{ProjectID: "test-project", Region: "us-central1"},
	}

	// --- Test Cases ---
	testCases := []struct {
		name                 string
		arch                 *servicemanager.MicroserviceArchitecture
		dataflowName         string
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
			expectedResourceKeys: []string{
				"pubsub_topic:tracer-topic",
				"pubsub_topic:verify-topic",
				"pubsub_subscription:tracer-sub",
			},
			expectedPolicies: map[string]iam.PolicyBinding{
				"pubsub_topic:tracer-topic": {
					ResourceType: "pubsub_topic", ResourceID: "tracer-topic",
					MemberRoles: map[string][]string{
						"roles/pubsub.publisher": {"serviceAccount:mock-pub-sa@project.iam.gserviceaccount.com"},
						"roles/pubsub.viewer":    {"serviceAccount:mock-pub-sa@project.iam.gserviceaccount.com", "serviceAccount:mock-sub-sa@project.iam.gserviceaccount.com"},
					},
				},
				"pubsub_topic:verify-topic": {
					ResourceType: "pubsub_topic", ResourceID: "verify-topic",
					MemberRoles: map[string][]string{
						"roles/pubsub.publisher": {"serviceAccount:mock-sub-sa@project.iam.gserviceaccount.com"},
						"roles/pubsub.viewer":    {"serviceAccount:mock-sub-sa@project.iam.gserviceaccount.com"},
					},
				},
				"pubsub_subscription:tracer-sub": {
					ResourceType: "pubsub_subscription", ResourceID: "tracer-sub",
					MemberRoles: map[string][]string{
						"roles/pubsub.subscriber": {"serviceAccount:mock-sub-sa@project.iam.gserviceaccount.com"},
						"roles/pubsub.viewer":     {"serviceAccount:mock-sub-sa@project.iam.gserviceaccount.com"},
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
			expectedResourceKeys: []string{},
			expectedPolicies:     map[string]iam.PolicyBinding{},
		},
	}

	// --- Run Test Cases ---
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// ARRANGE
			mockClient := &mockIAMClient{}
			iamManager, err := iam.NewIAMManager(mockClient, zerolog.Nop())
			require.NoError(t, err)

			// ACT
			_, err = iamManager.ApplyIAMForDataflow(context.Background(), tc.arch, tc.dataflowName)

			// ASSERT
			require.NoError(t, err)

			// Verify that the correct number of atomic policy updates were made.
			require.Len(t, mockClient.AppliedPolicies, len(tc.expectedResourceKeys))

			// Convert slice to map for easier lookup.
			actualPolicies := make(map[string]iam.PolicyBinding)
			for _, policy := range mockClient.AppliedPolicies {
				key := fmt.Sprintf("%s:%s", policy.ResourceType, policy.ResourceID)
				actualPolicies[key] = policy
			}

			// Verify the content of each applied policy.
			for _, key := range tc.expectedResourceKeys {
				t.Logf("Verifying policy for resource: %s", key)
				expectedPolicy, ok := tc.expectedPolicies[key]
				require.True(t, ok, "Expected policy for resource %s was not found", key)

				actualPolicy, ok := actualPolicies[key]
				require.True(t, ok, "Actual policy for resource %s was not applied", key)

				require.Equal(t, expectedPolicy.ResourceType, actualPolicy.ResourceType)
				require.Equal(t, expectedPolicy.ResourceID, actualPolicy.ResourceID)

				require.Len(t, actualPolicy.MemberRoles, len(expectedPolicy.MemberRoles))
				for role, expectedMembers := range expectedPolicy.MemberRoles {
					actualMembers, ok := actualPolicy.MemberRoles[role]
					require.True(t, ok, "Expected role %s not found in actual policy for %s", role, key)
					require.ElementsMatch(t, expectedMembers, actualMembers, "Member list for role %s on %s did not match", role, key)
				}
			}
		})
	}
}
