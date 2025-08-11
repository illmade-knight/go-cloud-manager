package servicemanager_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/api/googleapi"
)

// --- Mocks ---

type MockMessagingTopic struct{ mock.Mock }

func (m *MockMessagingTopic) ID() string { return m.Called().String(0) }
func (m *MockMessagingTopic) Exists(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}
func (m *MockMessagingTopic) Update(_ context.Context, _ servicemanager.TopicConfig) (*servicemanager.TopicConfig, error) {
	panic("mock not implemented")
}
func (m *MockMessagingTopic) Delete(ctx context.Context) error { return m.Called(ctx).Error(0) }

type MockMessagingSubscription struct{ mock.Mock }

func (m *MockMessagingSubscription) ID() string { return m.Called().String(0) }
func (m *MockMessagingSubscription) Exists(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}
func (m *MockMessagingSubscription) Update(ctx context.Context, cfg servicemanager.SubscriptionConfig) (*servicemanager.SubscriptionConfig, error) {
	args := m.Called(ctx, cfg)
	// REFACTOR_NOTE: The first returned value can be nil if the mock setup doesn't need to return a specific object.
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*servicemanager.SubscriptionConfig), args.Error(1)
}
func (m *MockMessagingSubscription) Delete(ctx context.Context) error { return m.Called(ctx).Error(0) }

type MockMessagingClient struct{ mock.Mock }

func (m *MockMessagingClient) Topic(id string) servicemanager.MessagingTopic {
	return m.Called(id).Get(0).(servicemanager.MessagingTopic)
}
func (m *MockMessagingClient) Subscription(id string) servicemanager.MessagingSubscription {
	return m.Called(id).Get(0).(servicemanager.MessagingSubscription)
}
func (m *MockMessagingClient) CreateTopic(_ context.Context, _ string) (servicemanager.MessagingTopic, error) {
	panic("mock not implemented")
}
func (m *MockMessagingClient) CreateTopicWithConfig(ctx context.Context, topicSpec servicemanager.TopicConfig) (servicemanager.MessagingTopic, error) {
	args := m.Called(ctx, topicSpec)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(servicemanager.MessagingTopic), args.Error(1)
}
func (m *MockMessagingClient) CreateSubscription(ctx context.Context, subSpec servicemanager.SubscriptionConfig) (servicemanager.MessagingSubscription, error) {
	args := m.Called(ctx, subSpec)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(servicemanager.MessagingSubscription), args.Error(1)
}
func (m *MockMessagingClient) Close() error { return m.Called().Error(0) }
func (m *MockMessagingClient) Validate(resources servicemanager.CloudResourcesSpec) error {
	return m.Called(resources).Error(0)
}

// --- Test Setup ---

func setupMessagingManagerTest(t *testing.T) (*servicemanager.MessagingManager, *MockMessagingClient) {
	mockClient := new(MockMessagingClient)
	logger := zerolog.Nop()
	env := servicemanager.Environment{Name: "test-env"}
	manager, err := servicemanager.NewMessagingManager(mockClient, logger, env)
	assert.NoError(t, err)
	assert.NotNil(t, manager)
	return manager, mockClient
}

func getTestMessagingResources() servicemanager.CloudResourcesSpec {
	return servicemanager.CloudResourcesSpec{
		Topics: []servicemanager.TopicConfig{
			{CloudResource: servicemanager.CloudResource{Name: "test-topic-1"}},
		},
		Subscriptions: []servicemanager.SubscriptionConfig{
			{
				CloudResource: servicemanager.CloudResource{Name: "test-sub-1"},
				Topic:         "test-topic-1",
			},
		},
	}
}

var notFoundErr = &googleapi.Error{Code: http.StatusNotFound, Message: "not found"}

// --- Tests ---

func TestMessagingManager_CreateResources_Success(t *testing.T) {
	manager, mockClient := setupMessagingManagerTest(t)
	resources := getTestMessagingResources()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Mocks
	mockClient.On("Validate", resources).Return(nil).Once()

	mockTopic := new(MockMessagingTopic)
	// The client will be asked for the topic twice: once for the topic creation, and once for the subscription dependency check.
	mockClient.On("Topic", "test-topic-1").Return(mockTopic)
	mockTopic.On("Exists", ctx).Return(false, nil).Once() // First check: topic doesn't exist
	mockClient.On("CreateTopicWithConfig", ctx, resources.Topics[0]).Return(mockTopic, nil).Once()

	mockSub := new(MockMessagingSubscription)
	mockClient.On("Subscription", "test-sub-1").Return(mockSub).Once()
	mockTopic.On("Exists", ctx).Return(true, nil).Once() // Second check: topic now exists for the subscription
	mockSub.On("Exists", ctx).Return(false, nil).Once()  // Subscription doesn't exist
	mockClient.On("CreateSubscription", ctx, resources.Subscriptions[0]).Return(mockSub, nil).Once()

	// Action
	provTopics, provSubs, err := manager.CreateResources(ctx, resources)

	// Assert
	assert.NoError(t, err)
	assert.Len(t, provTopics, 1)
	assert.Len(t, provSubs, 1)
	assert.Equal(t, "test-topic-1", provTopics[0].Name)
	mockClient.AssertExpectations(t)
	mockTopic.AssertExpectations(t)
	mockSub.AssertExpectations(t)
}

func TestMessagingManager_CreateResources_AlreadyExistsAndUpdate(t *testing.T) {
	manager, mockClient := setupMessagingManagerTest(t)
	resources := getTestMessagingResources()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Mocks
	mockClient.On("Validate", resources).Return(nil).Once()

	// Topic already exists, so it will be checked twice but not created.
	mockTopic := new(MockMessagingTopic)
	mockClient.On("Topic", "test-topic-1").Return(mockTopic)
	mockTopic.On("Exists", ctx).Return(true, nil).Times(2)

	// Subscription also exists, so it will be updated.
	mockSub := new(MockMessagingSubscription)
	mockClient.On("Subscription", "test-sub-1").Return(mockSub).Once()
	mockSub.On("Exists", ctx).Return(true, nil).Once()
	mockSub.On("Update", ctx, resources.Subscriptions[0]).Return(nil, nil).Once()

	// Action
	_, _, err := manager.CreateResources(ctx, resources)

	// Assert
	assert.NoError(t, err)
	mockClient.AssertNotCalled(t, "CreateTopicWithConfig")
	mockClient.AssertNotCalled(t, "CreateSubscription")
	mockClient.AssertExpectations(t)
	mockTopic.AssertExpectations(t)
	mockSub.AssertExpectations(t)
}

func TestMessagingManager_CreateResources_TopicForSubDoesNotExist(t *testing.T) {
	manager, mockClient := setupMessagingManagerTest(t)
	resources := getTestMessagingResources()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Mocks
	mockClient.On("Validate", resources).Return(nil).Once()
	mockTopic := new(MockMessagingTopic)
	mockClient.On("Topic", "test-topic-1").Return(mockTopic)
	// First call for topic creation passes (or seems to), but the crucial dependency check for the sub fails.
	mockTopic.On("Exists", ctx).Return(true, nil).Once()
	mockTopic.On("Exists", ctx).Return(false, nil).Once()

	// Action
	_, _, err := manager.CreateResources(ctx, resources)

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "topic 'test-topic-1' for subscription 'test-sub-1' does not exist")
	mockClient.AssertNotCalled(t, "CreateSubscription")
	mockClient.AssertExpectations(t)
}

func TestMessagingManager_Teardown_Success(t *testing.T) {
	manager, mockClient := setupMessagingManagerTest(t)
	resources := getTestMessagingResources()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Mocks
	mockTopic := new(MockMessagingTopic)
	mockClient.On("Topic", "test-topic-1").Return(mockTopic).Once()
	mockTopic.On("Delete", ctx).Return(nil).Once()

	mockSub := new(MockMessagingSubscription)
	mockClient.On("Subscription", "test-sub-1").Return(mockSub).Once()
	mockSub.On("Delete", ctx).Return(nil).Once()

	// Action
	err := manager.Teardown(ctx, resources)

	// Assert
	assert.NoError(t, err)
	mockClient.AssertExpectations(t)
}

func TestMessagingManager_Teardown_PartialFailure(t *testing.T) {
	manager, mockClient := setupMessagingManagerTest(t)
	resources := getTestMessagingResources()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()
	deleteErr := errors.New("permission denied")

	// Mocks
	mockTopic := new(MockMessagingTopic)
	mockClient.On("Topic", "test-topic-1").Return(mockTopic).Once()
	mockTopic.On("Delete", ctx).Return(deleteErr).Once() // Topic deletion fails

	mockSub := new(MockMessagingSubscription)
	mockClient.On("Subscription", "test-sub-1").Return(mockSub).Once()
	mockSub.On("Delete", ctx).Return(nil).Once() // Subscription deletion succeeds

	// Action
	err := manager.Teardown(ctx, resources)

	// Assert
	assert.NotNil(t, err)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete topic test-topic-1")
	assert.Contains(t, err.Error(), deleteErr.Error())
	mockClient.AssertExpectations(t)
}

func TestMessagingManager_Teardown_NotFoundIsIgnored(t *testing.T) {
	manager, mockClient := setupMessagingManagerTest(t)
	resources := getTestMessagingResources()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Mocks
	mockTopic := new(MockMessagingTopic)
	mockClient.On("Topic", "test-topic-1").Return(mockTopic).Once()
	mockTopic.On("Delete", ctx).Return(notFoundErr).Once()

	mockSub := new(MockMessagingSubscription)
	mockClient.On("Subscription", "test-sub-1").Return(mockSub).Once()
	mockSub.On("Delete", ctx).Return(notFoundErr).Once()

	// Action
	err := manager.Teardown(ctx, resources)

	// Assert
	assert.NoError(t, err) // Not found errors should not cause the teardown to fail
	mockClient.AssertExpectations(t)
}

func TestMessagingManager_Teardown_Protection(t *testing.T) {
	manager, mockClient := setupMessagingManagerTest(t)
	resources := getTestMessagingResources()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Enable teardown protection
	resources.Topics[0].CloudResource.TeardownProtection = true
	resources.Subscriptions[0].CloudResource.TeardownProtection = true

	// Action
	err := manager.Teardown(ctx, resources)

	// Assert
	assert.NoError(t, err)
	// Crucially, assert that the client was never asked to fetch the protected resources for deletion
	mockClient.AssertNotCalled(t, "Topic", mock.Anything)
	mockClient.AssertNotCalled(t, "Subscription", mock.Anything)
}

func TestMessagingManager_Verify_Failure(t *testing.T) {
	manager, mockClient := setupMessagingManagerTest(t)
	resources := getTestMessagingResources()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// Mocks
	mockTopic := new(MockMessagingTopic)
	mockClient.On("Topic", "test-topic-1").Return(mockTopic).Once()
	mockTopic.On("Exists", ctx).Return(true, nil).Once() // Topic exists

	mockSub := new(MockMessagingSubscription)
	mockClient.On("Subscription", "test-sub-1").Return(mockSub).Once()
	mockSub.On("Exists", ctx).Return(false, nil).Once() // Subscription does not exist

	// Action
	err := manager.Verify(ctx, resources)

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "subscription 'test-sub-1' not found")
	assert.NotContains(t, err.Error(), "topic 'test-topic-1' not found")
	mockClient.AssertExpectations(t)
}
