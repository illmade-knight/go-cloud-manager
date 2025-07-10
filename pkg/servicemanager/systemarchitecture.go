package servicemanager

import (
	"gopkg.in/yaml.v3"
	"time"
)

// This file defines the Go structs that map directly to the structure of the
// services.yaml file. The `yaml:"..."` tags are essential for the parser
// to know which YAML key corresponds to which Go struct field.

// LifecycleStrategy defines how the ServiceManager should treat a dataflow's resources.
type LifecycleStrategy string

const (
	// LifecycleStrategyPermanent indicates that resources are long-lived.
	LifecycleStrategyPermanent LifecycleStrategy = "permanent"
	// LifecycleStrategyEphemeral indicates that resources are temporary and should be torn down after use.
	LifecycleStrategyEphemeral LifecycleStrategy = "ephemeral"
)

// ResourceGroup defines a logical grouping of services that work together.
type ResourceGroup struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description,omitempty"`
	Services    map[string]ServiceSpec `yaml:"services"`
	Lifecycle   *LifecyclePolicy       `yaml:"lifecycle,omitempty"`
	Resources   CloudResourcesSpec     `yaml:"resources"`
}

// MicroserviceArchitecture is the root of the configuration structure.
type MicroserviceArchitecture struct {
	Environment
	Dataflows              map[string]ResourceGroup `yaml:"dataflows"`
	DeploymentEnvironments map[string]Environment   `yaml:"deployment_environments"`
}

// Environment holds configuration specific to a single environment (e.g., test, production).
type Environment struct {
	Name               string            `yaml:"name"`
	ProjectID          string            `yaml:"project_id"`
	Labels             map[string]string `yaml:"labels,omitempty"`
	Location           string            `yaml:"location,omitempty"`
	Region             string            `yaml:"region,omitempty"`
	TeardownProtection bool              `yaml:"teardown_protection,omitempty"`
}

// ServiceSpec defines a microservice's identity within the system.
type ServiceSpec struct {
	Name           string                 `yaml:"name"`
	Description    string                 `yaml:"description,omitempty"`
	ServiceAccount string                 `yaml:"service_account"`
	Metadata       map[string]interface{} `yaml:"metadata,omitempty"`
	Deployment     *DeploymentSpec        `yaml:"deployment,omitempty"` // <-- The new section
	HealthCheck    *HealthCheckSpec       `yaml:"health_check,omitempty"`
}

// DeploymentSpec defines how a service container should be deployed.
// This example is tailored for Google Cloud Run.
type DeploymentSpec struct {
	// Platform indicates the deployment target, e.g., "gcp-cloud-run".
	Platform string `yaml:"platform"`

	// Image is the full path to the Docker container image.
	// e.g., "us-central1-docker.pkg.dev/my-project/services/my-service:latest"
	Image string `yaml:"image"`

	// CPU and Memory resource allocations.
	CPU    string `yaml:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty"`

	// Scaling parameters for the service.
	MinInstances int64 `yaml:"min_instances,omitempty"`
	MaxInstances int64 `yaml:"max_instances,omitempty"`

	// EnvironmentVars are key-value pairs injected into the container as environment variables.
	EnvironmentVars map[string]string `yaml:"environment_vars,omitempty"`
}

// HealthCheckSpec defines the health check configuration for a service.
type HealthCheckSpec struct {
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

// LifecyclePolicy defines the lifecycle management rules for a dataflow.
type LifecyclePolicy struct {
	Strategy            LifecycleStrategy `yaml:"strategy"`
	KeepResourcesOnTest bool              `yaml:"keep_resources_on_test,omitempty"`
}

// CloudResourcesSpec is a container for all the cloud resources defined in the system.
type CloudResourcesSpec struct {
	Topics           []TopicConfig        `yaml:"topics"`
	Subscriptions    []SubscriptionConfig `yaml:"subscriptions"`
	BigQueryDatasets []BigQueryDataset    `yaml:"bigquery_datasets"`
	BigQueryTables   []BigQueryTable      `yaml:"bigquery_tables"`
	GCSBuckets       []GCSBucket          `yaml:"gcs_buckets"`
}

type IAMAccessType string

const (
	Service        IAMAccessType = "service"
	ServiceAccount IAMAccessType = "service-account"
)

// IAM defines how resources are accessed
type IAM struct {
	Name string        `yaml:"name"`
	Role string        `yaml:"role"` // e.g., "roles/pubsub.publisher", "roles/bigquery.dataEditor"
	Type IAMAccessType `yaml:"type"`
}

type CloudResource struct {
	Name               string            `yaml:"name"`
	Labels             map[string]string `yaml:"labels,omitempty"`
	IAMPolicy          []IAM             `yaml:"iam_access_policy,omitempty"`
	LifecycleRules     []LifecycleRule   `yaml:"lifecycle_rules,omitempty"`
	Description        string            `yaml:"description,omitempty"`
	TeardownProtection bool              `yaml:"teardown_protection,omitempty"`
}

// TopicConfig defines the configuration for a Pub/Sub topic.
type TopicConfig struct {
	CloudResource
	ProducerService string `yaml:"producer_service,omitempty"`
}

// SubscriptionConfig defines the configuration for a Pub/Sub subscription.
type SubscriptionConfig struct {
	CloudResource
	Topic              string           `yaml:"topic"`
	AckDeadlineSeconds int              `yaml:"ack_deadline_seconds,omitempty"`
	MessageRetention   Duration         `yaml:"message_retention_duration,omitempty"`
	RetryPolicy        *RetryPolicySpec `yaml:"retry_policy,omitempty"`
	ConsumerService    string           `yaml:"consumer_service,omitempty"`
}

// RetryPolicySpec should be updated to use the new Duration type.
type RetryPolicySpec struct {
	MinimumBackoff Duration `yaml:"minimum_backoff"`
	MaximumBackoff Duration `yaml:"maximum_backoff"`
}

// BigQueryDataset defines the configuration for a BigQuery dataset.
type BigQueryDataset struct {
	CloudResource
	Location string `yaml:"location,omitempty"`
}

// BigQueryTable defines the configuration for a BigQuery table.
type BigQueryTable struct {
	CloudResource
	Dataset                string   `yaml:"dataset"`
	SchemaSourceType       string   `yaml:"schema_source_type"`
	SchemaSourceIdentifier string   `yaml:"schema_source_identifier"`
	TimePartitioningField  string   `yaml:"time_partitioning_field,omitempty"`
	TimePartitioningType   string   `yaml:"time_partitioning_type,omitempty"`
	ClusteringFields       []string `yaml:"clustering_fields,omitempty"`
	Expiration             Duration `yaml:"expiration,omitempty"`
}

// GCSBucket defines the configuration for a GCS bucket.
type GCSBucket struct {
	CloudResource
	Location          string `yaml:"location,omitempty"`
	StorageClass      string `yaml:"storage_class,omitempty"`
	VersioningEnabled bool   `yaml:"versioning_enabled,omitempty"`
}

// LifecycleAction represents an action in a lifecycle rule.
type LifecycleAction struct {
	Type string // e.g., "Delete"
}

// LifecycleCondition represents the conditions for a lifecycle rule.
type LifecycleCondition struct {
	AgeInDays int
	// Other common conditions can be added here (e.g., CreatedBefore, Liveness).
}

// LifecycleRule combines an action and a condition.
type LifecycleRule struct {
	Action    LifecycleAction
	Condition LifecycleCondition
}

// Duration is a custom type that wraps time.Duration to implement yaml.Unmarshaler.
type Duration time.Duration

// UnmarshalYAML implements the yaml.Unmarshaler interface, allowing "15s" to be parsed directly to a duration.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
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

// ProvisionedResources contains the details of all resources created by a setup operation.
type ProvisionedResources struct {
	Topics           []ProvisionedTopic
	Subscriptions    []ProvisionedSubscription
	GCSBuckets       []ProvisionedGCSBucket
	BigQueryDatasets []ProvisionedBigQueryDataset
	BigQueryTables   []ProvisionedBigQueryTable
}
