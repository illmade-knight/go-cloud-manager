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

// ISchedulerManager defines the interface for a CloudSchedulerManager.
// Its CreateResources method does not return any provisioned resources, only an error.
type ISchedulerManager interface {
	IManager
	CreateResources(ctx context.Context, resources CloudResourcesSpec) error
}

// IServiceManager defines the high-level interface for the main ServiceManager,
// allowing it to be mocked for testing purposes.
type IServiceManager interface {
	SetupFoundationalDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) (*ProvisionedResources, error)
	SetupDependentDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) error
	TeardownDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) error
	TeardownAll(ctx context.Context, arch *MicroserviceArchitecture) error
	VerifyDataflow(ctx context.Context, arch *MicroserviceArchitecture, dataflowName string) error
	SetupAll(ctx context.Context, arch *MicroserviceArchitecture) (*ProvisionedResources, error)
}
