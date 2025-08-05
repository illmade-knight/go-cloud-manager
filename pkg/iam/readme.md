Excellent. All files for the `iam` package have been received and analyzed. It's a sophisticated companion to the `servicemanager` package, focusing on intelligent and automated IAM policy management.

Here is the comprehensive `readme.md` for the `iam` package.

-----

# IAM Manager

The `iam` package is a powerful Go library for managing Google Cloud IAM policies in a declarative and automated way. It is designed to work as a companion to the `servicemanager` package, using the same architecture definition files to understand resource relationships and apply the principle of least privilege.

## Overview

After provisioning infrastructure with the `servicemanager`, the next critical step is to configure permissions. This package automates that step. It introduces a `RolePlanner` that analyzes your architecture's YAML configuration to infer the necessary IAM roles for each microservice. It can grant permissions based on both explicit declarations in your YAML and intelligent conventions, significantly reducing boilerplate and the risk of misconfiguration.

**Core Workflow:**

1.  **Define Architecture:** Use the same YAML files from the `servicemanager` package. Enhance them with explicit `iam_policy` blocks or rely on the planner's conventions.
2.  **Plan:** The `RolePlanner` creates a complete map of which service account needs which role on which resource.
3.  **Execute:** The `IAMManager` takes this plan and uses a resource-aware `IAMClient` to apply the bindings across various GCP services (Pub/Sub, GCS, BigQuery, Cloud Run, Secret Manager, etc.).

## Core Concepts

* **Intelligent Role Planning:** The `RolePlanner` is the heart of the package. It automatically determines the required permissions by inspecting the architecture in two ways:
    * **Implicit Role Inference:** It understands the relationships between your services and resources. For example:
        * A service listed as a `producer_service` for a topic automatically gets `roles/pubsub.publisher` on that topic.
        * A service listed as a `consumer_service` for a subscription gets `roles/pubsub.subscriber`.
        * A service with a `dependency` on another service gets `roles/run.invoker` for that dependency.
        * A service configured to use `secret_environment_vars` gets `roles/secretmanager.secretAccessor` for each secret.
    * **Explicit Policy Definition:** You can explicitly define permissions within a resource's configuration using the `iam_policy` block.
* **Resource-Aware Client:** Applying IAM policies varies across GCP services. The `GoogleIAMClient` abstracts these differences away. It acts as a single entry point that knows how to correctly apply policies to BigQuery datasets (via ACLs), Cloud Run services (via the Run API), and other resources (via the standard `iam.Handle`).
* **Service Account Lifecycle Management:** The package includes a `ServiceAccountManager` to programmatically create, delete, and manage service accounts and a `IAMProjectManager` to handle project-level IAM bindings.
* **Test-Friendly Design:** The package is designed for robust testing. It features a `TestIAMClient` with a service account pooling mechanism to work around GCP's strict IAM API quotas, making integration tests faster and more reliable.

## Getting Started

The `iam` package is typically used after the `servicemanager` has provisioned the underlying resources.

### Step 1: Define IAM in Your Architecture

In your `dataflow.yaml` files, specify the permissions your services need.

**Example `dataflow.yaml` with IAM definitions:**

```yaml
name: "data-processing-flow"
lifecycle:
  strategy: ephemeral

services:
  # This service will get roles implicitly from the resource links below.
  processor-service:
    name: "processor-service"
    service_account: "processor-sa"
    # This dependency will grant 'processor-sa' the 'roles/run.invoker' on 'downstream-service'.
    dependencies:
      - "downstream-service"
    deployment:
      secret_environment_vars:
        # This will grant 'processor-sa' the 'roles/secretmanager.secretAccessor' role on 'my-api-key'.
        - name: API_KEY
          value_from: "my-api-key"

  # This service has no implicit links, so we grant its role explicitly.
  viewer-service:
    name: "viewer-service"
    service_account: "viewer-sa"

resources:
  topics:
    - name: "processing-topic"
      # This link implicitly grants 'processor-sa' the 'roles/pubsub.publisher' role.
      producer_service: "processor-service"
      
  gcs_buckets:
    - name: "processed-data-bucket"
      iam_policy:
        # This block explicitly grants 'viewer-sa' the 'roles/storage.objectViewer' role.
        - name: "viewer-service"
          role: "roles/storage.objectViewer"
```

### Step 2: Write the Go Application

In your main application, after setting up your resources, instantiate and run the `IAMManager`.

**`main.go` (continued from `servicemanager` example)**

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager" // Assumed path
	"github.com/rs/zerolog"
)

func main() {
    // ... (ServiceManager setup code from previous README)
    // Assume `arch` is the loaded and hydrated MicroserviceArchitecture struct.

    logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
    ctx := context.Background()

    // 1. Create the IAM client.
    iamClient, err := iam.NewGoogleIAMClient(ctx, arch.ProjectID)
    if err != nil {
        log.Fatalf("Failed to create IAM client: %v", err)
    }
    defer iamClient.Close()

    // 2. Create the IAM manager.
    iamManager, err := iam.NewIAMManager(iamClient, logger)
    if err != nil {
        log.Fatalf("Failed to create IAM manager: %v", err)
    }

    // 3. Loop through all services in the architecture and apply their IAM policies.
    log.Println("Applying all IAM policies...")
    for dfName, dataflow := range arch.Dataflows {
        for serviceName := range dataflow.Services {
            err := iamManager.ApplyIAMForService(ctx, arch, dfName, serviceName)
            if err != nil {
                log.Fatalf("Failed to apply IAM for service '%s' in dataflow '%s': %v", serviceName, dfName, err)
            }
        }
    }
    log.Println("IAM policies applied successfully.")
}
```

## Advanced: Test Client with Service Account Pooling

To mitigate IAM API quota issues during integration tests, the package provides a `TestIAMClient`. Instead of creating and deleting service accounts for each test, this client "leases" accounts from a pre-created pool.

**How it works:**

1.  **Discovery:** On initialization, the client finds all service accounts in your project with a specific prefix (e.g., `it-iam-`).
2.  **Leasing:** When a test calls `EnsureServiceAccountExists`, the client finds an unused, "clean" account from the pool and provides it to the test.
3.  **Returning:** A call to `DeleteServiceAccount` is faked; it simply marks the account as available again in the pool.
4.  **Cleanup:** The client's `Close()` method is critical. It iterates through all accounts used during the test run and removes any IAM bindings that were added, returning them to a pristine state for the next run.

**Usage in a test:**

```go
//go:build integration

package iam_test

import (
    "context"
    "testing"
    
    "github.com/illmade-knight/go-cloud-manager/pkg/iam"
    "github.com/rs/zerolog"
)

func TestSomethingWithIAM(t *testing.T) {
    projectID := // ... get your project ID
    ctx := context.Background()
    logger := zerolog.Nop()

    // Use the TestIAMClient instead of the real one.
    // All service accounts created will have the prefix "my-test-sa-".
    iamClient, err := iam.NewTestIAMClient(ctx, projectID, logger, "my-test-sa-")
    require.NoError(t, err)
    
    // The Close() method performs the critical cleanup.
    t.Cleanup(func() { _ = iamClient.Close() })

    iamManager, err := iam.NewIAMManager(iamClient, logger)
    require.NoError(t, err)

    // ... run your test logic using iamManager ...
}
```