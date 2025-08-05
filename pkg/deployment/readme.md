# Cloud Deployment Package

The `deployment` package provides a high-level, automated workflow for building Go source code into a container image and deploying it as a serverless application on Google Cloud Run. It orchestrates Google Cloud Build, Artifact Registry, and Cloud Storage to provide a seamless "source-to-URL" experience.

## Overview

This package is designed to be the deployment engine for a larger orchestration system, such as the `servicemanager`. It takes a deployment specification—which includes the source code path, desired image repository, and resource settings—and handles every step of the process, from packaging the source to deploying the final, running service.

### Core Workflow

The main `CloudBuildDeployer` encapsulates the entire build-and-deploy lifecycle. A single call to its `Deploy` method performs the following sequence of operations:

1.  **Ensure Prerequisites:** It automatically checks for the existence of a GCS bucket for source code and an Artifact Registry repository for container images. If they don't exist, it creates them.
2.  **Package Source:** It walks the local source code directory and packages it into a `.tar.gz` archive in memory.
3.  **Upload Source:** The source code archive is uploaded to the designated GCS bucket.
4.  **Trigger Build:** A Google Cloud Build job is triggered. This build uses Cloud Native Buildpacks (`gcr.io/k8s-skaffold/pack`) to compile the Go source code and build a production-ready container image without requiring a `Dockerfile`.
5.  **Monitor Build:** The deployer waits for the Cloud Build job to complete, reporting failure if the build does not succeed.
6.  **Deploy to Cloud Run:** Upon a successful build, the newly created container image is deployed to Google Cloud Run. The deployer will create the service if it doesn't exist or update it with a new revision if it does.
7.  **Return URL:** The public URL of the successfully deployed service is returned.

## Key Features

* **Dockerfile-Free Builds:** Leverages Google's Cloud Native Buildpacks to automatically detect the Go runtime, compile the code, and produce an optimized, secure container image without needing a manually written `Dockerfile`.
* **Monorepo Support:** Contains critical, built-in logic to correctly build services located in subdirectories of a monorepo. It automatically handles the copying of root `go.mod` and `go.sum` files to ensure the build environment can resolve dependencies.
* **Automated Prerequisite Management:** Simplifies setup by automatically creating the GCS bucket for source uploads and the Artifact Registry repository for images if they are not already present.
* **Idempotent Deployments:** The deployment process is idempotent. Running a deployment for an existing service will safely update it to a new version, while running it for a new service will create it.
* **Clean Abstractions:** The `CloudBuildDeployer` relies on interfaces for its dependencies (storage, build, deploy), making the core logic fully unit-testable with mocks.

## Usage

The `CloudBuildDeployer` is the primary entry point for the package.

### Example

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/illmade-knight/go-cloud-manager/pkg/deployment" // Assumed package paths
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

func main() {
	ctx := context.Background()
	logger := zerolog.New(os.Stdout)

	// Basic configuration
	projectID := "your-gcp-project-id"
	region := "us-central1"
	sourceBucket := "your-project-id_cloudbuild" // Convention used by gcloud

	// 1. Instantiate the deployer
	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, region, sourceBucket, logger)
	if err != nil {
		log.Fatalf("Failed to create deployer: %v", err)
	}

	// 2. Define the deployment spec for a service
	// This spec is compatible with the servicemanager package.
	spec := servicemanager.DeploymentSpec{
		SourcePath:          "./cmd/my-service", // Path to the service's source code
		BuildableModulePath: "cmd/my-service",   // Relative path for monorepo builds
		ImageRepo:           "my-app-images",    // Artifact Registry repository name
		Region:              region,
		CPU:                 "1",
		Memory:              "512Mi",
		MinInstances:        0,
		MaxInstances:        10,
	}
    
    serviceName := "my-awesome-service"
    serviceAccountEmail := "my-service-sa@your-gcp-project-id.iam.gserviceaccount.com"

	// 3. Deploy the service
	log.Printf("Deploying service '%s'...", serviceName)
	serviceURL, err := deployer.Deploy(ctx, serviceName, serviceAccountEmail, spec)
	if err != nil {
		log.Fatalf("Deployment failed: %v", err)
	}
	log.Printf("Service deployed successfully! URL: %s", serviceURL)
    
    // 4. Teardown the service when no longer needed
    // log.Printf("Tearing down service '%s'...", serviceName)
    // if err := deployer.Teardown(ctx, serviceName); err != nil {
    //     log.Fatalf("Teardown failed: %v", err)
    // }
    // log.Println("Teardown successful.")
}
```

## IAM Prerequisites

For the deployment process to succeed, the **default Cloud Build service account** needs several IAM roles at the project level. This service account has an email address in the format `[PROJECT_NUMBER]@cloudbuild.gserviceaccount.com`.

Ensure this service account has the following roles:

* `roles/cloudbuild.serviceAgent`: Allows Cloud Build to act on behalf of the project.
* `roles/storage.objectViewer`: Allows Cloud Build to read the source code from the GCS bucket.
* `roles/artifactregistry.writer`: Allows Cloud Build to push the built container image to Artifact Registry.
* `roles/run.admin`: Allows Cloud Build to deploy services to Cloud Run.
* `roles/iam.serviceAccountUser`: Allows Cloud Build to attach a service account to the new Cloud Run service.

The integration tests for this package include a helper function, `ensureCloudBuildPermissions`, that can programmatically grant these roles.