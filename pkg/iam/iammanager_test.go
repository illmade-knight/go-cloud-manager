package iam_test

import (
	"context"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockIAMClient remains the same.
type MockIAMClient struct {
	mock.Mock
}

func (m *MockIAMClient) EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error) {
	args := m.Called(ctx, accountName)
	return args.String(0), args.Error(1)
}

func (m *MockIAMClient) AddResourceIAMBinding(ctx context.Context, binding iam.IAMBinding, member string) error {
	args := m.Called(ctx, binding, member)
	return args.Error(0)
}
func (m *MockIAMClient) RemoveResourceIAMBinding(ctx context.Context, binding iam.IAMBinding, member string) error {
	return nil
}
func (m *MockIAMClient) AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error {
	return nil
}
func (m *MockIAMClient) DeleteServiceAccount(ctx context.Context, accountName string) error {
	return nil
}
func (m *MockIAMClient) AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error {
	return nil
}
func (m *MockIAMClient) Close() error {
	return nil
}

func TestIAMManager_ApplyIAMForService(t *testing.T) {
	// --- Arrange ---
	mockClient := new(MockIAMClient)
	logger := zerolog.Nop()

	testServiceAccount := "test-sa-for-iam"
	testServiceAccountEmail := "test-sa-for-iam@my-project.iam.gserviceaccount.com"
	testMemberString := "serviceAccount:" + testServiceAccountEmail

	// The test now defines the full architecture that the manager needs for planning.
	arch := &servicemanager.MicroserviceArchitecture{
		Dataflows: map[string]servicemanager.ResourceGroup{
			"test-dataflow": {
				Services: map[string]servicemanager.ServiceSpec{
					"test-service": {
						Name:           "test-service",
						ServiceAccount: testServiceAccount,
					},
				},
				Resources: servicemanager.CloudResourcesSpec{
					Topics: []servicemanager.TopicConfig{
						{
							CloudResource: servicemanager.CloudResource{
								Name: "my-topic",
								IAMPolicy: []servicemanager.IAM{
									{Name: "test-service", Role: "roles/pubsub.publisher"},
								},
							},
						},
					},
					GCSBuckets: []servicemanager.GCSBucket{
						{
							CloudResource: servicemanager.CloudResource{
								Name: "my-bucket",
								IAMPolicy: []servicemanager.IAM{
									{Name: "test-service", Role: "roles/storage.objectAdmin"},
								},
							},
						},
					},
				},
			},
		},
	}

	// The manager is now created with the architecture.
	iamManager, err := iam.NewIAMManager(mockClient, arch, logger)
	require.NoError(t, err)

	// Set up mock expectations.
	mockClient.On("EnsureServiceAccountExists", mock.Anything, testServiceAccount).
		Return(testServiceAccountEmail, nil).Once()

	// The test expects the manager to have planned and now be executing the bindings.
	// We use mock.MatchedBy to check the struct fields without worrying about order.
	mockClient.On("AddResourceIAMBinding", mock.Anything, mock.MatchedBy(func(b iam.IAMBinding) bool {
		return b.ResourceType == "pubsub_topic" && b.ResourceID == "my-topic" && b.Role == "roles/pubsub.publisher"
	}), testMemberString).Return(nil).Once()

	mockClient.On("AddResourceIAMBinding", mock.Anything, mock.MatchedBy(func(b iam.IAMBinding) bool {
		return b.ResourceType == "gcs_bucket" && b.ResourceID == "my-bucket" && b.Role == "roles/storage.objectAdmin"
	}), testMemberString).Return(nil).Once()

	// --- Act ---
	// The call signature remains the same, so no changes are needed in the DeploymentManager.
	err = iamManager.ApplyIAMForService(context.Background(), arch.Dataflows["test-dataflow"], "test-service")

	// --- Assert ---
	require.NoError(t, err)
	mockClient.AssertExpectations(t)
}
