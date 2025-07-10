package deployment_test

import (
	"context"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockIAMClient now implements the new, simpler interface.
type MockIAMClient struct {
	mock.Mock
}

func (m *MockIAMClient) EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error) {
	args := m.Called(ctx, accountName)
	return args.String(0), args.Error(1)
}

func (m *MockIAMClient) AddResourceIAMBinding(ctx context.Context, resourceType, resourceID, role, member string) error {
	args := m.Called(ctx, resourceType, resourceID, role, member)
	return args.Error(0)
}

func (m *MockIAMClient) RemoveResourceIAMBinding(ctx context.Context, resourceType, resourceID, role, member string) error {
	args := m.Called(ctx, resourceType, resourceID, role, member)
	return args.Error(0)
}

func TestIAMManager_ApplyIAMForService(t *testing.T) {
	// --- Arrange ---
	mockClient := new(MockIAMClient)
	logger := zerolog.Nop()
	iamManager := deployment.NewIAMManager(mockClient, logger)

	testServiceAccount := "test-sa-for-iam"
	testServiceAccountEmail := "test-sa-for-iam@my-project.iam.gserviceaccount.com"
	testMemberString := "serviceAccount:" + testServiceAccountEmail

	dataflow := servicemanager.ResourceGroup{
		Name: "test-dataflow",
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
	}

	// Set up mock expectations, now without the projectID argument.
	mockClient.On("EnsureServiceAccountExists", mock.Anything, testServiceAccount).
		Return(testServiceAccountEmail, nil).Once()

	mockClient.On("AddResourceIAMBinding", mock.Anything, "pubsub_topic", "my-topic", "roles/pubsub.publisher", testMemberString).
		Return(nil).Once()

	mockClient.On("AddResourceIAMBinding", mock.Anything, "gcs_bucket", "my-bucket", "roles/storage.objectAdmin", testMemberString).
		Return(nil).Once()

	// --- Act ---
	// The call to the manager is now simpler.
	err := iamManager.ApplyIAMForService(context.Background(), dataflow, "test-service")

	// --- Assert ---
	require.NoError(t, err)
	mockClient.AssertExpectations(t)
}
