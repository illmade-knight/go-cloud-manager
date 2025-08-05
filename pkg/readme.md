# Go Cloud Infrastructure Manager

This project is a comprehensive, Go-based system for declaratively managing, deploying, and securing complex microservice architectures on Google Cloud Platform. It treats your entire cloud environment as code, integrating resource provisioning, IAM policy management, and application deployment into a cohesive, automated workflow.

## Package Architecture

The project is organized into four distinct packages, each with a clear responsibility, forming a layered and decoupled architecture.

### `/servicemanager` - Resource Lifecycle Management

This is the foundational layer responsible for managing the lifecycle (create, update, verify, teardown) of core cloud infrastructure resources.

* **Function:** It directly interacts with GCP APIs for services like Pub/Sub, Google Cloud Storage, and BigQuery to bring them to a desired state.
* **Configuration:** Its behavior is driven by a set of YAML files that define the entire system architecture.
* **Design:** It uses a powerful adapter pattern, abstracting the GCP-specific clients behind interfaces. This makes the package highly testable and potentially extensible to other cloud providers.

### `/deployment` - Build & Deploy Engine

This package acts as the CI/CD engine for the system. It takes application source code and handles the entire process of building it into a container and deploying it to Cloud Run.

* **Function:** It orchestrates a workflow that involves packaging source code, uploading it to GCS, triggering a Google Cloud Build job, and finally creating or updating a Cloud Run service with the new image.
* **Prerequisites:** It automatically creates and manages prerequisites, such as the Artifact Registry repository needed to store container images.
* **Buildpacks:** It uses Cloud Native Buildpacks (`pack`) by default, allowing it to build standard Go applications without requiring a `Dockerfile`.

### `/iam` - Permissions & Security

This package is dedicated to automating Identity and Access Management (IAM). It ensures that all services and resources are configured with the correct, least-privilege permissions to interact with each other securely.

* **Function:** It provides managers for creating service accounts and applying IAM policy bindings to a wide variety of GCP resources, including Pub/Sub topics, GCS buckets, BigQuery datasets, and Cloud Run services.
* **Intelligence:** Its key feature is the `RolePlanner`, which analyzes the architecture definition to automatically infer many of the required IAM roles from conventions (e.g., a service listed as a `producer_service` gets publisher permissions). This drastically reduces the amount of manual IAM configuration required.

### `/orchestration` - Top-Level Workflow Coordination

This is the highest-level package that acts as the main entry point and brain of the system. It integrates all other packages to execute a complete, end-to-end deployment of an entire architecture.

* **Function:** It introduces a **Service Director** pattern, where a central service is first deployed into the cloud. This director then acts as a remote agent, receiving commands via Pub/Sub to manage the lifecycle of other application "dataflows".
* **Workflow:** The `Conductor` component manages a flexible, multi-phase workflow that includes setting up IAM, deploying the Service Director, commanding the director to provision resources, and finally deploying all the application services.

## How They Fit Together

The packages are layered to create a clear separation of concerns, with dependencies flowing from the higher-level orchestration down to the resource-specific management.

1.  The **`/orchestration`** package is the primary entry point for a user.
2.  It uses the **`/iam`** package to first set up all necessary service accounts and permissions.
3.  It then uses the **`/deployment`** package to build and deploy the services (both the central Service Director and the final application services).
4.  The deployed Service Director contains the logic from the **`/servicemanager`** package, which it uses to provision and manage the dataflow resources as commanded by the orchestrator.
5.  All packages operate on the common configuration schema defined in `/servicemanager/systemarchitecture.go`, which acts as the single source of truth for the entire system.