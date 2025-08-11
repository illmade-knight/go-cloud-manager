package servicemanager

import "context"

type IManager interface {
	Teardown(ctx context.Context, resources CloudResourcesSpec) error
	Verify(ctx context.Context, resources CloudResourcesSpec) error
}

// IBigQueryManager defines the interface for a BigQueryManager.
type IBigQueryManager interface {
	IManager
	CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedBigQueryTable, []ProvisionedBigQueryDataset, error)
}

// IMessagingManager defines the interface for a MessagingManager.
type IMessagingManager interface {
	IManager
	CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedTopic, []ProvisionedSubscription, error)
}

// IStorageManager defines the interface for a StorageManager.
type IStorageManager interface {
	IManager
	CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedGCSBucket, error)
}

// IFirestoreManager defines the interface for a FirestoreManager.
type IFirestoreManager interface {
	IManager
	CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedFirestoreDatabase, error)
}
