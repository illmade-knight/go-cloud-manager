# Service Manager

The `servicemanager` is a powerful and extensible Go package for declaratively managing cloud infrastructure on Google Cloud Platform (GCP). It provides a configuration-driven approach to provision, verify, and tear down complex microservice architectures, treating infrastructure as a core component of your application's Go code.

## Overview

This package solves the problem of managing the lifecycle of cloud resources that support a microservice architecture. Instead of using separate tools or manual processes, you can define your services, Pub/Sub topics, GCS buckets, BigQuery datasets, and their relationships in YAML files. A central `ServiceManager` then orchestrates the creation and deletion of these resources in a concurrent, idempotent, and safe manner.

**Core Workflow:**

1.  **Define:** Describe your entire system—services and their cloud resource dependencies—in a set of YAML files.
2.  **Load:** The `yamlio` loader parses your YAML configuration into Go structs.
3.  **Hydrate:** Before provisioning, a "hydration" step dynamically enriches your configuration, generating unique container image tags and injecting resource names as environment variables into the services that need them.
4.  **Orchestrate:** The `ServiceManager` uses specialized sub-managers (`MessagingManager`, `StorageManager`, `BigQueryManager`) to provision, verify, or tear down the defined resources concurrently on GCP.

## Core Concepts

The package is built on several key architectural principles:

* **Declarative Configuration:** You declare the *desired state* of your architecture in YAML files. The `ServiceManager` is responsible for making the cloud environment match that state. The Go structs in `systemarchitecture.go` serve as the schema for these files.
* **Managers & Orchestration:** A top-level `ServiceManager` acts as the main entry point. It delegates tasks to resource-specific managers (e.g., `MessagingManager`, `StorageManager`), which contain the logic for handling a specific service like Pub/Sub or GCS.
* **Abstraction & Extensibility:** The package heavily uses interfaces (e.g., `MessagingClient`, `StorageClient`) to decouple the orchestration logic from the underlying cloud provider SDKs. The concrete implementations for Google Cloud (e.g., `gcpMessagingClientAdapter`) act as adapters, making the system easy to test and potentially extend to other cloud providers.
* **Idempotency:** All resource creation and update operations are idempotent. Running the setup process multiple times will not cause errors; it will simply update existing resources to match the configuration or skip them if they are already in the desired state.
* **Lifecycle Management:** To prevent accidental data loss, resource groups can be marked with a `lifecycle` strategy.
    * `ephemeral`: Resources in this group will be deleted during a teardown operation.
    * `permanent`: Resources are protected and will be skipped during a teardown.
    * Individual resources can also be protected using the `teardown_protection: true` flag.
* **Schema Registry:** The `BigQueryManager` uses a dynamic schema registry. You can define your table schemas as Go structs in your own application code and register them with the `ServiceManager` using an `init()` function. This decouples the manager from application-specific data models.

## Getting Started

Here is a step-by-step guide to using the `ServiceManager`.

### Step 1: Define Your Architecture (YAML)

Create a set of YAML files to describe your system. Start with a "hub" file for environment-wide settings, and then create separate files for each logical group of resources (a "dataflow").

**`hub.yaml`**

```yaml
# hub.yaml: Defines the global environment for the architecture.
name: "my-awesome-app"
project_id: "your-gcp-project-id"
region: "us-central1"        # Default region for services like Cloud Run
location: "US"               # Default location for resources like BigQuery or GCS
teardown_protection: false   # Global flag to prevent accidental deletion

service_manager_spec:
  name: "service-manager"
  service_account: "service-manager-sa@your-gcp-project-id.iam.gserviceaccount.com"
  deployment:
    source_path: "./cmd/servicemanager" # Path to the ServiceManager's own code
```

**`dataflow_ingestion.yaml`**

```
# dataflow_ingestion.yaml: A logical grouping of services and resources.
name: "ingestion-pipeline"
  
    description: "Handles incoming telemetry data."
    lifecycle:
      strategy: ephemeral # This entire group of resources can be torn down.
    
    services:
      ingest-service:
        name: "ingest-service"
        service_account: "ingest-sa@your-gcp-project-id.iam.gserviceaccount.com"
        deployment:
          source_path: "./cmd/ingest"
          # Env vars for topic/subscription will be auto-injected by hydration
      
      storage-service:
        name: "storage-service"
        service_account: "storage-sa@your-gcp-project-id.iam.gserviceaccount.com"
        deployment:
          source_path: "./cmd/storage"
    
    resources:
      topics:
        - name: "telemetry-topic"
          # The manager will inject TELEMETRY_TOPIC_ID into 'ingest-service'
          producer_service: "ingest-service" 
    
      subscriptions:
        - name: "telemetry-sub-for-storage"
          topic: "telemetry-topic"
          # The manager will inject TELEMETRY_SUB_FOR_STORAGE_SUB_ID into 'storage-service'
          consumer_service: "storage-service"
          ack_deadline_seconds: 60
    
      gcs_buckets:
        - name: "telemetry-archive-bucket"
          location: "US"
          storage_class: "STANDARD"
          lifecycle_rules:
            - action: { type: "Delete" }
              condition: { age_in_days: 90 }
    
      bigquery_datasets:
        - name: "telemetry_dataset"
          location: "US"
          teardown_protection: true # This dataset will not be deleted on teardown.
      
      bigquery_tables:
        - name: "raw_events"
          dataset: "telemetry_dataset"
          schema_type: "MyEventSchema" # Must be registered in Go code
          time_partitioning_field: "timestamp"
          time_partitioning_type: "DAY"

```

### Step 2: (Optional) Register BigQuery Schemas

If you use BigQuery tables, you need to register their Go struct schemas. Do this in an `init()` function in the package where the schema is defined.

**`schemas/events.go`**

```
package schemas

import (
    "time"
    "github.com/your-org/your-repo/servicemanager"
)

type MyEvent struct {
    ID        string    `bigquery:"id"`
    Data      string    `bigquery:"data"`
    Timestamp time.Time `bigquery:"timestamp"`
}

func init() {
    // Register the schema with a unique name.
    servicemanager.RegisterSchema("MyEventSchema", MyEvent{})
}
```

### Step 3: Write the Go Application

Create a `main.go` file to drive the `ServiceManager`.

**`main.go`**

```
package main

import (
	"context"
	"log"
	"os"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager" // Assuming this is the package path
	"github.com/rs/zerolog"
)

func main() {
	// Use a logger for structured output
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctx := context.Background()

	// 1. Initialize the YAML loader with paths to your config files.
	io, err := servicemanager.NewYAMLArchitectureIO(
		"hub.yaml",
		[]string{"dataflow_ingestion.yaml"},
		"provisioned-resources.yaml", // Output file path
	)
	if err != nil {
		log.Fatal(err)
	}

	// 2. Load the architecture from the files.
	arch, err := io.LoadArchitecture(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// 3. Hydrate the architecture to fill in dynamic values.
	// The "runID" is optional and can be used to create unique names for this execution.
	runID := "dev-run-123"
	err = servicemanager.HydrateArchitecture(arch, "my-image-repo", runID, logger)
	if err != nil {
		log.Fatal(err)
	}

	// 4. Create the ServiceManager. It will automatically initialize clients
	// for the resource types found in your architecture.
	sm, err := servicemanager.NewServiceManager(ctx, arch, nil, logger)
	if err != nil {
		log.Fatal(err)
	}

	// --- Choose an action ---

	// To set up ALL resources for ALL dataflows:
	log.Println("Starting full environment setup...")
	if _, err := sm.SetupAll(ctx, arch); err != nil {
		log.Fatalf("Setup failed: %v", err)
	}
	log.Println("Setup completed successfully.")

	// To tear down ONLY the ephemeral dataflows:
	// log.Println("Starting teardown of ephemeral resources...")
	// if err := sm.TeardownAll(ctx, arch); err != nil {
	// 	log.Fatalf("Teardown failed: %v", err)
	// }
	// log.Println("Teardown completed successfully.")
}

```

## Testing

The package has a comprehensive testing strategy with three distinct layers, controlled by Go build tags.

* **Unit Tests:** Test individual components in isolation using mocks. They are fast and run by default.
  ```sh
  go test ./...
  ```
* **Integration Tests (`integration` tag):** These tests run against live, in-memory emulators for GCP services (Pub/Sub, GCS, BigQuery) to validate the interaction between components without hitting real cloud resources.
  ```sh
  go test -tags=integration ./...
  ```
* **Cloud Integration Tests (`cloud_integration` tag):** These tests provision and tear down *real* resources in a GCP project, providing full end-to-end validation. They require active GCP credentials.
  ```sh
  go test -tags=cloud_integration ./...
  ```