package servicemanager_test

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// --- Mocks ---

type MockStorageBucketHandle struct{ mock.Mock }

func (m *MockStorageBucketHandle) Attrs(ctx context.Context) (*servicemanager.BucketAttributes, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*servicemanager.BucketAttributes), args.Error(1)
}
func (m *MockStorageBucketHandle) Create(ctx context.Context, projectID string, attrs *servicemanager.BucketAttributes) error {
	return m.Called(ctx, projectID, attrs).Error(0)
}
func (m *MockStorageBucketHandle) Update(ctx context.Context, attrs servicemanager.BucketAttributesToUpdate) (*servicemanager.BucketAttributes, error) {
	args := m.Called(ctx, attrs)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*servicemanager.BucketAttributes), args.Error(1)
}
func (m *MockStorageBucketHandle) Delete(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *MockStorageBucketHandle) IAM() *iam.Handle {
	return m.Called().Get(0).(*iam.Handle)
}

type MockStorageClient struct{ mock.Mock }

func (m *MockStorageClient) Bucket(name string) servicemanager.StorageBucketHandle {
	return m.Called(name).Get(0).(servicemanager.StorageBucketHandle)
}
func (m *MockStorageClient) Buckets(ctx context.Context, projectID string) servicemanager.BucketIterator {
	panic("not implemented")
}
func (m *MockStorageClient) Close() error {
	return m.Called().Error(0)
}

// --- Test Setup ---

func setupStorageManagerTest(t *testing.T) (*servicemanager.StorageManager, *MockStorageClient) {
	mockClient := new(MockStorageClient)
	logger := zerolog.Nop()
	env := servicemanager.Environment{Name: "test-env", ProjectID: "test-project"}
	manager, err := servicemanager.NewStorageManager(mockClient, logger, env)
	assert.NoError(t, err)
	assert.NotNil(t, manager)
	return manager, mockClient
}

func getTestStorageResources() servicemanager.CloudResourcesSpec {
	return servicemanager.CloudResourcesSpec{
		GCSBuckets: []servicemanager.GCSBucket{
			{CloudResource: servicemanager.CloudResource{Name: "test-bucket-1"}},
			{CloudResource: servicemanager.CloudResource{Name: "test-bucket-2"}},
		},
	}
}

// --- Tests ---

func TestNewStorageManager(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		setupStorageManagerTest(t)
	})

	t.Run("Nil Client", func(t *testing.T) {
		_, err := servicemanager.NewStorageManager(nil, zerolog.Nop(), servicemanager.Environment{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "storage client (StorageClient interface) cannot be nil")
	})
}

func TestStorageManager_CreateResources(t *testing.T) {
	ctx := context.Background()

	t.Run("Success - Create New", func(t *testing.T) {
		manager, mockClient := setupStorageManagerTest(t)
		resources := getTestStorageResources()

		// Mocks for two new buckets
		mockHandle1 := new(MockStorageBucketHandle)
		mockHandle2 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-1").Return(mockHandle1).Once()
		mockClient.On("Bucket", "test-bucket-2").Return(mockHandle2).Once()

		mockHandle1.On("Attrs", ctx).Return(nil, servicemanager.Done).Once() // Not exist
		mockHandle1.On("Create", ctx, "test-project", mock.Anything).Return(nil).Once()
		mockHandle2.On("Attrs", ctx).Return(nil, servicemanager.Done).Once() // Not exist
		mockHandle2.On("Create", ctx, "test-project", mock.Anything).Return(nil).Once()

		provisioned, err := manager.CreateResources(ctx, resources)

		assert.NoError(t, err)
		assert.Len(t, provisioned, 2)
		mockClient.AssertExpectations(t)
		mockHandle1.AssertExpectations(t)
		mockHandle2.AssertExpectations(t)
	})

	t.Run("Success - Update Existing", func(t *testing.T) {
		manager, mockClient := setupStorageManagerTest(t)
		resources := getTestStorageResources()

		mockHandle1 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-1").Return(mockHandle1).Once()
		mockHandle1.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once() // Exists
		mockHandle1.On("Update", ctx, mock.Anything).Return(&servicemanager.BucketAttributes{}, nil).Once()

		mockHandle2 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-2").Return(mockHandle2).Once()
		mockHandle2.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once() // Exists
		mockHandle2.On("Update", ctx, mock.Anything).Return(&servicemanager.BucketAttributes{}, nil).Once()

		_, err := manager.CreateResources(ctx, resources)

		assert.NoError(t, err)
		mockHandle1.AssertNotCalled(t, "Create", mock.Anything, mock.Anything) // Ensure create is not called
		mockClient.AssertExpectations(t)
	})

	t.Run("Partial Failure - One Fails to Create", func(t *testing.T) {
		manager, mockClient := setupStorageManagerTest(t)
		resources := getTestStorageResources()
		createErr := errors.New("invalid bucket name")

		mockHandle1 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-1").Return(mockHandle1).Once()
		mockHandle1.On("Attrs", ctx).Return(nil, servicemanager.Done).Once()
		mockHandle1.On("Create", ctx, mock.Anything, mock.Anything).Return(nil).Once() // Succeeds

		mockHandle2 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-2").Return(mockHandle2).Once()
		mockHandle2.On("Attrs", ctx).Return(nil, servicemanager.Done).Once()
		mockHandle2.On("Create", ctx, mock.Anything, mock.Anything).Return(createErr).Once() // Fails

		provisioned, err := manager.CreateResources(ctx, resources)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create bucket 'test-bucket-2'")
		assert.Len(t, provisioned, 1) // Only the successful one should be returned
		assert.Equal(t, "test-bucket-1", provisioned[0].Name)
		mockClient.AssertExpectations(t)
	})
}

func TestStorageManager_Teardown(t *testing.T) {
	ctx := context.Background()

	t.Run("Success", func(t *testing.T) {
		manager, mockClient := setupStorageManagerTest(t)
		resources := getTestStorageResources()

		mockHandle1 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-1").Return(mockHandle1).Once()
		mockHandle1.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once()
		mockHandle1.On("Delete", ctx).Return(nil).Once()

		mockHandle2 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-2").Return(mockHandle2).Once()
		mockHandle2.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once()
		mockHandle2.On("Delete", ctx).Return(nil).Once()

		err := manager.Teardown(ctx, resources)

		assert.NoError(t, err)
		mockClient.AssertExpectations(t)
	})

	t.Run("Teardown Protection Enabled", func(t *testing.T) {
		manager, mockClient := setupStorageManagerTest(t)
		resources := getTestStorageResources()
		resources.GCSBuckets[0].TeardownProtection = true // Protect the first bucket

		mockHandle2 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-2").Return(mockHandle2).Once() // Expect call only for bucket 2
		mockHandle2.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once()
		mockHandle2.On("Delete", ctx).Return(nil).Once()

		err := manager.Teardown(ctx, resources)

		assert.NoError(t, err)
		mockClient.AssertNotCalled(t, "Bucket", "test-bucket-1") // Ensure protected bucket is not touched
		mockClient.AssertExpectations(t)
	})

	t.Run("Failure - Bucket Not Empty", func(t *testing.T) {
		manager, mockClient := setupStorageManagerTest(t)
		resources := getTestStorageResources()
		notEmptyErr := errors.New("googleapi: Error 409: The bucket you tried to delete is not empty")

		mockHandle1 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-1").Return(mockHandle1).Once()
		mockHandle1.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once()
		mockHandle1.On("Delete", ctx).Return(notEmptyErr).Once()

		mockHandle2 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-2").Return(mockHandle2).Once()
		mockHandle2.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once()
		mockHandle2.On("Delete", ctx).Return(nil).Once() // This one succeeds

		err := manager.Teardown(ctx, resources)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to delete bucket 'test-bucket-1' because it is not empty")
		mockClient.AssertExpectations(t)
	})
}

func TestStorageManager_Verify(t *testing.T) {
	ctx := context.Background()

	t.Run("Success", func(t *testing.T) {
		manager, mockClient := setupStorageManagerTest(t)
		resources := getTestStorageResources()

		mockHandle1 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-1").Return(mockHandle1).Once()
		mockHandle1.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once()

		mockHandle2 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-2").Return(mockHandle2).Once()
		mockHandle2.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once()

		err := manager.Verify(ctx, resources)

		assert.NoError(t, err)
		mockClient.AssertExpectations(t)
	})

	t.Run("Failure - One Bucket Missing", func(t *testing.T) {
		manager, mockClient := setupStorageManagerTest(t)
		resources := getTestStorageResources()

		mockHandle1 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-1").Return(mockHandle1).Once()
		mockHandle1.On("Attrs", ctx).Return(&servicemanager.BucketAttributes{}, nil).Once() // Exists

		mockHandle2 := new(MockStorageBucketHandle)
		mockClient.On("Bucket", "test-bucket-2").Return(mockHandle2).Once()
		mockHandle2.On("Attrs", ctx).Return(nil, servicemanager.Done).Once() // Does not exist

		err := manager.Verify(ctx, resources)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bucket 'test-bucket-2' not found")
		assert.NotContains(t, err.Error(), "test-bucket-1")
		mockClient.AssertExpectations(t)
	})
}
