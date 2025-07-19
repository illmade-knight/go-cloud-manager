package servicemanager_test

import (
	"context"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/stretchr/testify/mock"
)

// --- Mocks for Manager Interfaces ---

type MockMessagingManager struct{ mock.Mock }

func (m *MockMessagingManager) CreateResources(ctx context.Context, resources servicemanager.CloudResourcesSpec) ([]servicemanager.ProvisionedTopic, []servicemanager.ProvisionedSubscription, error) {
	args := m.Called(ctx, resources)
	return args.Get(0).([]servicemanager.ProvisionedTopic), args.Get(1).([]servicemanager.ProvisionedSubscription), args.Error(2)
}
func (m *MockMessagingManager) Teardown(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}
func (m *MockMessagingManager) Verify(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}

type MockStorageManager struct{ mock.Mock }

func (m *MockStorageManager) CreateResources(ctx context.Context, resources servicemanager.CloudResourcesSpec) ([]servicemanager.ProvisionedGCSBucket, error) {
	args := m.Called(ctx, resources)
	return args.Get(0).([]servicemanager.ProvisionedGCSBucket), args.Error(1)
}
func (m *MockStorageManager) Teardown(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}
func (m *MockStorageManager) Verify(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}

type MockBigQueryManager struct{ mock.Mock }

func (m *MockBigQueryManager) CreateResources(ctx context.Context, resources servicemanager.CloudResourcesSpec, schemaRegistry map[string]interface{}) ([]servicemanager.ProvisionedBigQueryTable, []servicemanager.ProvisionedBigQueryDataset, error) {
	args := m.Called(ctx, resources)
	return args.Get(0).([]servicemanager.ProvisionedBigQueryTable), args.Get(1).([]servicemanager.ProvisionedBigQueryDataset), args.Error(2)
}
func (m *MockBigQueryManager) Teardown(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}
func (m *MockBigQueryManager) Verify(ctx context.Context, resources servicemanager.CloudResourcesSpec) error {
	return m.Called(ctx, resources).Error(0)
}
