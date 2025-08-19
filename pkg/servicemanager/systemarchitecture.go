package servicemanager

import (
	"time"

	"gopkg.in/yaml.v3"
)

// This file defines the Go structs that map directly to the structure of the
// services.yaml file. The `yaml:"..."` tags are essential for the parser
// to know which YAML key corresponds to which Go struct field.

// --- Root and Environment Configuration ---

// MicroserviceArchitecture is the root of the configuration structure, defining the entire system.
type MicroserviceArchitecture struct {
	// Environment holds default configuration for the entire architecture,
	// such as project ID and default region.
	Environment `yaml:",inline"`
	// ServiceManagerSpec defines the special service responsible for orchestrating
	// the creation and management of all other resources.
	ServiceManagerSpec ServiceSpec `yaml:"service_manager_spec"`
	// Dataflows is a map of logical resource groups. Each key is a dataflow name.
	Dataflows map[string]ResourceGroup `yaml:"dataflows"`
	// DeploymentEnvironments allows for overriding environment settings for specific
	// named environments (e.g., 'staging', 'production').
	DeploymentEnvironments map[string]Environment `yaml:"deployment_environments"`
}

// Environment holds configuration specific to a single environment (e.g., test, production).
type Environment struct {
	// Name is the identifier for the environment (e.g., "dev", "prod").
	Name string `yaml:"name"`
	// ProjectID is the cloud project identifier (e.g., "my-gcp-project-12345").
	ProjectID string `yaml:"project_id"`
	// Region is the default geographic region for deploying resources (e.g., "us-central1").
	Region string `yaml:"region,omitempty"`
	// Location is the default geographic location for resources that are region-agnostic,
	// like some storage buckets or BigQuery datasets (e.g., "US").
	Location string `yaml:"location,omitempty"`
	// Labels are key-value pairs to apply to all resources by default.
	Labels map[string]string `yaml:"labels,omitempty"`
	// TeardownProtection, if true, prevents accidental deletion of resources in this environment.
	TeardownProtection bool `yaml:"teardown_protection,omitempty"`
}

// --- Service and Deployment Specification ---

// ServiceSpec defines a single microservice's identity and deployment characteristics.
type ServiceSpec struct {
	// Name is the unique identifier for the service (e.g., "user-authentication-service").
	Name string `yaml:"name"`
	// Description provides a human-readable explanation of the service's purpose.
	Description string `yaml:"description,omitempty"`
	// ServiceAccount is the name of the identity (IAM Service Account) the service will run as.
	ServiceAccount string `yaml:"service_account"`
	// Deployment specifies how the service should be built and deployed (e.g., as a Cloud Run container).
	Deployment *DeploymentSpec `yaml:"deployment,omitempty"`
	// Dependencies lists other services that this service needs to communicate with.
	Dependencies []string `yaml:"dependencies,omitempty"`
	// HealthCheck defines the endpoint for monitoring the service's health.
	HealthCheck *HealthCheckSpec `yaml:"health_check,omitempty"`
	// Metadata holds arbitrary key-value data about the service.
	Metadata map[string]interface{} `yaml:"metadata,omitempty"`
}

// DeploymentSpec defines how a service container should be deployed.
// This example is tailored for Google Cloud Run.
type DeploymentSpec struct {
	// SourcePath is the local filesystem path to the service's source code.
	SourcePath string `yaml:"source_path"`
	// BuildableModulePath specifies the sub-directory within the source path to build.
	// e.g., "./cmd/myservice"
	BuildableModulePath string `yaml:"buildable_module_path,omitempty"`

	// REFACTOR_NOTE: The following fields are populated during hydration.
	// They are not typically set in the source YAML file.

	// Image is the full path to the Docker container image after it has been built.
	// e.g., "us-central1-docker.pkg.dev/my-project/services/my-service:latest"
	Image string `yaml:"image"`
	// ImageRepo is the name of the artifact repository where the image is stored.
	ImageRepo string `yaml:"image_repo,omitempty"`
	// ServiceURL is the public-facing URL of the deployed service.
	ServiceURL string `yaml:"service_url"`
	// Region is the specific region where the service will be deployed.
	Region string `yaml:"region,omitempty"`

	// Platform indicates the deployment target (e.g., "gcp-cloud-run").
	Platform string `yaml:"platform"`
	// CPU and Memory resource allocations. (e.g., CPU: "1", Memory: "512Mi")
	CPU    string `yaml:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty"`

	// Scaling parameters for the service.
	MinInstances int64 `yaml:"min_instances,omitempty"`
	MaxInstances int64 `yaml:"max_instances,omitempty"`

	// EnvironmentVars are key-value pairs injected into the container as environment variables.
	EnvironmentVars map[string]string `yaml:"environment_vars,omitempty"`
	// SecretEnvironmentVars maps environment variables to secrets stored in a secret manager.
	SecretEnvironmentVars []SecretEnvVar `yaml:"secret_environment_vars,omitempty"`
}

// SecretEnvVar defines a mapping from a container environment variable to a cloud secret.
type SecretEnvVar struct {
	// Name is the environment variable name inside the container (e.g., "API_KEY").
	Name string `yaml:"name"`
	// ValueFrom is the platform-specific ID of the secret (e.g., "my-api-key-secret" in GCP Secret Manager).
	ValueFrom string `yaml:"value_from"`
}

// HealthCheckSpec defines the health check configuration for a service.
type HealthCheckSpec struct {
	// Port the health check is exposed on.
	Port int `yaml:"port"`
	// Path is the URI path for the health check endpoint (e.g., "/healthz").
	Path string `yaml:"path"`
}

// --- Resource Grouping and Lifecycle ---

// ResourceGroup defines a logical grouping of services and the cloud resources they depend on.
type ResourceGroup struct {
	// Name is the unique identifier for the dataflow or resource group.
	Name string `yaml:"name,omitempty"`
	// Description provides a human-readable explanation of the group's purpose.
	Description string `yaml:"description,omitempty"`
	// Services is a map of service specifications belonging to this group. The key is a logical name for the service.
	Services map[string]ServiceSpec `yaml:"services"`
	// Resources defines all the cloud infrastructure (topics, buckets, etc.) required by the services in this group.
	Resources CloudResourcesSpec `yaml:"resources"`
	// Lifecycle defines the teardown strategy for the resources in this group.
	Lifecycle *LifecyclePolicy `yaml:"lifecycle,omitempty"`
}

// LifecyclePolicy defines the lifecycle management rules for a dataflow.
type LifecyclePolicy struct {
	// Strategy determines if resources are temporary or long-lived.
	Strategy LifecycleStrategy `yaml:"strategy"`
	// KeepResourcesOnTest, if true, prevents teardown even for ephemeral resources in a test environment.
	KeepResourcesOnTest bool `yaml:"keep_resources_on_test,omitempty"`
}

// LifecycleStrategy defines how the ServiceManager should treat a dataflow's resources.
type LifecycleStrategy string

const (
	// LifecycleStrategyPermanent indicates that resources are long-lived and protected from teardown.
	LifecycleStrategyPermanent LifecycleStrategy = "permanent"
	// LifecycleStrategyEphemeral indicates that resources are temporary and should be torn down after use.
	LifecycleStrategyEphemeral LifecycleStrategy = "ephemeral"
)

// --- Cloud Resource Specifications ---

// CloudResource is a base struct embedded in all specific resource types, providing common fields.
type CloudResource struct {
	// Name is the unique identifier for the resource.
	Name string `yaml:"name"`
	// Description provides a human-readable explanation of the resource's purpose.
	Description string `yaml:"description,omitempty"`
	// Labels are key-value pairs for organizing and filtering resources.
	Labels map[string]string `yaml:"labels,omitempty"`
	// IAMPolicy defines who has what type of access to this resource.
	IAMPolicy []IAM `yaml:"iam_access_policy,omitempty"`
	// LifecycleRules define rules for automated resource management (e.g., deleting old objects).
	LifecycleRules []LifecycleRule `yaml:"lifecycle_rules,omitempty"`
	// TeardownProtection, if true, prevents this specific resource from being deleted.
	TeardownProtection bool `yaml:"teardown_protection,omitempty"`
}

// CloudResourcesSpec is a container for all the cloud resources defined in a ResourceGroup.
type CloudResourcesSpec struct {
	Topics               []TopicConfig         `yaml:"topics,omitempty"`
	Subscriptions        []SubscriptionConfig  `yaml:"subscriptions,omitempty"`
	BigQueryDatasets     []BigQueryDataset     `yaml:"bigquery_datasets,omitempty"`
	BigQueryTables       []BigQueryTable       `yaml:"bigquery_tables,omitempty"`
	GCSBuckets           []GCSBucket           `yaml:"gcs_buckets,omitempty"`
	FirestoreDatabases   []FirestoreDatabase   `yaml:"firestore_databases,omitempty"`
	FirestoreCollections []FirestoreCollection `yaml:"firestore_collections,omitempty"`
}

// TopicConfig defines the configuration for a Pub/Sub topic.
type TopicConfig struct {
	CloudResource `yaml:",inline"`
	// ProducerService is the logical name of the service (defined in the same ResourceGroup) that publishes to this topic.
	// This is used by the hydration step to inject the topic name as an environment variable into the service.
	ProducerService *ServiceMapping `yaml:"producer_service,omitempty"`
}

type LookupMethod string

const (
	LookupEnv      LookupMethod = "env"
	LookupYAML     LookupMethod = "yaml"
	LookupMethodDB LookupMethod = "db"
)

type Lookup struct {
	Key    string       `yaml:"key"`
	Method LookupMethod `yaml:"method"`
}

// ServiceMapping defines a mapping between a Resource and the service that uses it. The service uses the
// Lookup to identify the resource - this could be an environment variable, a config file, a database etc
type ServiceMapping struct {
	Name   string `yaml:"name"`
	Lookup Lookup `yaml:"lookup,omitempty"`
}

// SubscriptionConfig defines the configuration for a Pub/Sub subscription.
type SubscriptionConfig struct {
	CloudResource `yaml:",inline"`
	// Topic is the name of the Pub/Sub topic this subscription is attached to.
	Topic string `yaml:"topic"`
	// ConsumerService is the logical name of the service that consumes messages from this subscription.
	// This is used by the hydration step to inject the subscription name as an environment variable.
	ConsumerService *ServiceMapping `yaml:"consumer_service,omitempty"`
	// AckDeadlineSeconds is the time a consumer has to acknowledge a message before it's redelivered.
	AckDeadlineSeconds int `yaml:"ack_deadline_seconds,omitempty"`
	// MessageRetention is how long Pub/Sub retains unacknowledged messages. (e.g., "7d", "24h").
	MessageRetention Duration `yaml:"message_retention_duration,omitempty"`
	// RetryPolicy defines the backoff strategy for redelivering messages.
	RetryPolicy *RetryPolicySpec `yaml:"retry_policy,omitempty"`
}

// RetryPolicySpec defines the minimum and maximum backoff for a subscription's retry policy.
type RetryPolicySpec struct {
	// MinimumBackoff is the shortest time to wait before redelivering. (e.g., "10s").
	MinimumBackoff Duration `yaml:"minimum_backoff"`
	// MaximumBackoff is the longest time to wait before redelivering. (e.g., "600s").
	MaximumBackoff Duration `yaml:"maximum_backoff"`
}

// BigQueryDataset defines the configuration for a BigQuery dataset.
type BigQueryDataset struct {
	CloudResource `yaml:",inline"`
	// Location is the geographic location of the dataset (e.g., "US", "EU").
	Location string `yaml:"location,omitempty"`
}

// LifecycleRule combines an action and a condition for resource lifecycle management (e.g., GCS objects).
type LifecycleRule struct {
	Action    LifecycleAction    `yaml:"action"`
	Condition LifecycleCondition `yaml:"condition"`
}

// LifecycleAction represents an action in a lifecycle rule.
type LifecycleAction struct {
	// Type is the action to perform (e.g., "Delete", "SetStorageClass").
	Type string `yaml:"type"`
}

// LifecycleCondition represents the conditions for a lifecycle rule.
type LifecycleCondition struct {
	// AgeInDays triggers the action when an object is older than this value.
	AgeInDays int `yaml:"age_in_days,omitempty"`
	// Other common conditions can be added here (e.g., CreatedBefore, Liveness).
}

// --- IAM and Provisioned Resource Structs ---

// IAM defines an access control policy for a resource.
type IAM struct {
	// Name is the identifier of the principal. The meaning depends on the `Type`.
	// If Type is "service", this is the logical name of a service in the same ResourceGroup.
	// If Type is "service-account", this is the full email of a service account.
	Name string `yaml:"name"`
	// Role is the cloud IAM role to grant (e.g., "roles/pubsub.publisher", "roles/bigquery.dataEditor").
	Role string `yaml:"role"`
	// Type specifies whether the Name refers to a service within the architecture or an explicit service account.
	Type IAMAccessType `yaml:"type"`
}

// IAMAccessType distinguishes the type of principal in an IAM policy.
type IAMAccessType string

const (
	// Service indicates the IAM principal is a service defined in the architecture.
	Service IAMAccessType = "service"
	// ServiceAccount indicates the IAM principal is an external service account email.
	ServiceAccount IAMAccessType = "service-account"
)

// ProvisionedResources contains details of all resources created by a setup operation.
// This struct is used to pass information between resource creation and IAM policy application steps.
type ProvisionedResources struct {
	Topics             []ProvisionedTopic
	Subscriptions      []ProvisionedSubscription
	GCSBuckets         []ProvisionedGCSBucket
	BigQueryDatasets   []ProvisionedBigQueryDataset
	BigQueryTables     []ProvisionedBigQueryTable
	FirestoreDatabases []ProvisionedFirestoreDatabase
}

// ProvisionedTopic holds details of a created topic.
type ProvisionedTopic struct {
	Name            string
	ProducerService string
}

// ProvisionedSubscription holds details of a created Pub/Sub subscription.
type ProvisionedSubscription struct {
	Name  string
	Topic string
}

// ProvisionedGCSBucket holds details of a created GCS bucket.
type ProvisionedGCSBucket struct {
	Name string
}

// ProvisionedBigQueryDataset holds details of a created BigQuery dataset.
type ProvisionedBigQueryDataset struct {
	Name string
}

// ProvisionedBigQueryTable holds details of a created BigQuery table.
type ProvisionedBigQueryTable struct {
	Dataset string
	Name    string
}

// ProvisionedFirestoreDatabase holds details of a created Firestore database.
type ProvisionedFirestoreDatabase struct {
	Name string
}

// FirestoreDatabaseType defines the mode of the Firestore database.
type FirestoreDatabaseType string

const (
	// FirestoreModeNative is the standard, recommended Firestore mode.
	FirestoreModeNative FirestoreDatabaseType = "NATIVE"
	// FirestoreModeDatastore is the Datastore compatibility mode for legacy applications.
	FirestoreModeDatastore FirestoreDatabaseType = "DATASTORE_MODE"
)

type BigQueryTable struct {
	CloudResource `yaml:",inline"`
	// Consumers lists the services that read from this table.
	// IAM: This implicitly grants the service account the 'roles/bigquery.dataViewer' role.
	Consumers []ServiceMapping `yaml:"consumers,omitempty"`
	// Producers lists the services that write to this table.
	// IAM: This implicitly grants the service account the 'roles/bigquery.dataEditor' role.
	Producers []ServiceMapping `yaml:"producers,omitempty"`
	// Dataset is the name of the parent BigQuery dataset.
	Dataset string `yaml:"dataset"`
	// SchemaType is a key that maps to a Go struct registered with the ServiceManager.
	// The manager uses this to infer the table schema.
	SchemaType string `yaml:"schema_type"`
	// SchemaImportPath is a reserved field for future reflection-based schema lookups.
	SchemaImportPath string `yaml:"schema_import_path"`
	// TimePartitioningField is the column used to partition the table by time.
	TimePartitioningField string `yaml:"time_partitioning_field,omitempty"`
	// TimePartitioningType is the granularity of the time partitioning (e.g., "DAY", "HOUR").
	TimePartitioningType string `yaml:"time_partitioning_type,omitempty"`
	// ClusteringFields are the columns used to cluster data within partitions for faster queries.
	ClusteringFields []string `yaml:"clustering_fields,omitempty"`
	// Expiration defines how long the table's data is kept. (e.g., "90d").
	Expiration Duration `yaml:"expiration,omitempty"`
}

// GCSBucket defines the configuration for a Google Cloud Storage bucket.
type GCSBucket struct {
	CloudResource `yaml:",inline"`
	// Consumers lists the services that read from this bucket.
	// IAM: This implicitly grants the service account the 'roles/storage.objectViewer' role.
	Consumers []ServiceMapping `yaml:"consumers,omitempty"`
	// Producers lists the services that write to this bucket.
	// IAM: This implicitly grants the service account the 'roles/storage.objectAdmin' role.
	Producers []ServiceMapping `yaml:"producers,omitempty"`
	// Location is the geographic location of the bucket (e.g., "US-CENTRAL1").
	Location string `yaml:"location,omitempty"`
	// StorageClass is the default storage class for objects in the bucket (e.g., "STANDARD", "NEARLINE").
	StorageClass string `yaml:"storage_class,omitempty"`
	// VersioningEnabled, if true, keeps a history of objects in the bucket.
	VersioningEnabled bool `yaml:"versioning_enabled,omitempty"`
}

// FirestoreDatabase defines the configuration for a Cloud Firestore database.
type FirestoreDatabase struct {
	CloudResource `yaml:",inline"`
	// LocationID specifies the multi-region or region for the database (e.g., "eur3", "nam5").
	LocationID string `yaml:"location_id"`
	// Type specifies the database mode, either NATIVE or DATASTORE_MODE.
	Type FirestoreDatabaseType `yaml:"type"`
	// Consumers lists the services that read from this database.
	// IAM: This implicitly grants the service account the 'roles/datastore.viewer' role.
	Consumers []ServiceMapping `yaml:"consumers,omitempty"`
	// Producers lists the services that write to this database.
	// IAM: This implicitly grants the service account the 'roles/datastore.user' role.
	Producers []ServiceMapping `yaml:"producers,omitempty"`
}

type FirestoreCollection struct {
	CloudResource `yaml:",inline"`
	// Consumers lists the services that read from this collection.
	// IAM: collections do not have IAM so this is used for Hydration etc
	Consumers []ServiceMapping `yaml:"consumers,omitempty"`
	// Producers lists the services that write to this collection.
	// IAM: collections do not have IAM so this is used for Hydration etc
	Producers []ServiceMapping `yaml:"producers,omitempty"`
	// FirestoreDatabase is the name of the parent Firestore database.
	FirestoreDatabase string `yaml:"database"`
}

// Duration is a custom type that wraps time.Duration to implement yaml.Unmarshaler,
// allowing human-readable strings like "15s" or "2h" to be parsed directly from YAML.
type Duration time.Duration

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	decodeErr := value.Decode(&s)
	if decodeErr != nil {
		return decodeErr
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}
