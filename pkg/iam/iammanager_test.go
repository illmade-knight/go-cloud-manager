package iam_test

import (
	"context"
	"testing"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockIAMClient provides a mock implementation of the IAMClient interface for testing.
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
	args := m.Called(ctx, binding, member)
	return args.Error(0)
}

func (m *MockIAMClient) AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error {
	args := m.Called(ctx, location, repositoryID, role, member)
	return args.Error(0)
}

func (m *MockIAMClient) DeleteServiceAccount(ctx context.Context, accountName string) error {
	args := m.Called(ctx, accountName)
	return args.Error(0)
}

func (m *MockIAMClient) AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error {
	args := m.Called(ctx, serviceAccountEmail, member, role)
	return args.Error(0)
}

func (m *MockIAMClient) Close() error {
	args := m.Called()
	return args.Error(0)
}

func TestIAMManager_ApplyIAMForService(t *testing.T) {
	// --- Arrange ---
	mockClient := new(MockIAMClient)
	logger := zerolog.Nop()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	testServiceAccount := "test-sa-for-iam"
	testServiceAccountEmail := "test-sa-for-iam@my-project.iam.gserviceaccount.com"
	testMemberString := "serviceAccount:" + testServiceAccountEmail

	// Define a test architecture where the planner will find two roles for the service.
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

	iamManager, err := iam.NewIAMManager(mockClient, logger)
	require.NoError(t, err)

	// Set up mock expectations.
	// 1. Expect the manager to ensure the service account exists.
	mockClient.On("EnsureServiceAccountExists", ctx, testServiceAccount).
		Return(testServiceAccountEmail, nil).Once()

	// 2. Expect the manager to apply the publisher role binding.
	mockClient.On("AddResourceIAMBinding", ctx, mock.MatchedBy(func(b iam.IAMBinding) bool {
		return b.ResourceType == "pubsub_topic" && b.ResourceID == "my-topic" && b.Role == "roles/pubsub.publisher"
	}), testMemberString).Return(nil).Once()

	// 3. Expect the manager to apply the storage admin role binding.
	mockClient.On("AddResourceIAMBinding", ctx, mock.MatchedBy(func(b iam.IAMBinding) bool {
		return b.ResourceType == "gcs_bucket" && b.ResourceID == "my-bucket" && b.Role == "roles/storage.objectAdmin"
	}), testMemberString).Return(nil).Once()

	// --- Act ---
	err = iamManager.ApplyIAMForService(ctx, arch, "test-dataflow", "test-service")

	// --- Assert ---
	require.NoError(t, err)
	mockClient.AssertExpectations(t)
}
