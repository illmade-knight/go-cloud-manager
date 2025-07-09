package servicemanager

import "context"

// IBigQueryManager defines the interface for a BigQueryManager.
type IBigQueryManager interface {
	CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedBigQueryTable, []ProvisionedBigQueryDataset, error)
	Teardown(ctx context.Context, resources CloudResourcesSpec) error
	Verify(ctx context.Context, resources CloudResourcesSpec) error
}

// IMessagingManager defines the interface for a MessagingManager.
type IMessagingManager interface {
	CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedTopic, []ProvisionedSubscription, error)
	Teardown(ctx context.Context, resources CloudResourcesSpec) error
	Verify(ctx context.Context, resources CloudResourcesSpec) error
}

// IStorageManager defines the interface for a StorageManager.
type IStorageManager interface {
	CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedGCSBucket, error)
	Teardown(ctx context.Context, resources CloudResourcesSpec) error
	Verify(ctx context.Context, resources CloudResourcesSpec) error
}
