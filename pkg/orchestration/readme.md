# Cloud Orchestration Package

The `orchestration` package provides the top-level coordination logic for deploying and managing an entire microservice architecture on Google Cloud. It integrates the `servicemanager`, `iam`, and `deployment` packages into a cohesive, phase-based workflow.

## Overview

This package implements a powerful and scalable architectural pattern centered around a **Service Director**. Instead of running all infrastructure and deployment logic from a local machine or a single CI/CD job, this package first deploys a central "Service Director" application into your cloud environment. This director then acts as a remote agent, receiving commands via Pub/Sub to manage the lifecycle of other services and resources.

This decoupled, event-driven approach provides enhanced security, scalability, and resilience against transient failures.

### The Conductor Workflow

The `Conductor` is the main, user-facing entry point. It executes a multi-phase workflow to stand up an entire environment. The workflow is designed to be efficient by running long-running tasks in parallel and robust by explicitly waiting for cloud resource propagation.

The standard, end-to-end workflow is:

1.  **Phase 1: Setup IAM**: The local `Conductor` creates all necessary service account identities (for both the Service Director and the applications). It then enters a polling loop to **wait for these new accounts to propagate** and become visible across all of Google Cloud's systems. This is a critical prerequisite step.

2.  **Phase 2: Parallel Build & Remote Setup**: The `Conductor` starts two long-running operations at the same time:
    * **Task A (Local Build)**: It builds all the container images for the dataflow applications.
    * **Task B (Remote Setup)**: It deploys the `ServiceDirector` application to Cloud Run. Once the director is healthy, the `Conductor` commands it to create all necessary cloud resources (e.g., Pub/Sub topics) and **apply the IAM policies** that link the application service accounts to those resources.

3.  **Phase 3: Verify IAM Policy Propagation**: After the `ServiceDirector` signals that its work is complete, the `Conductor` enters another polling loop. This time, it waits for the **IAM policy bindings** (applied by the remote director) to become globally consistent and visible.

4.  **Phase 4: Final Deployment**: Once the container images from Task A are ready and the IAM policies from Phase 3 are verified, the `Conductor` performs the final, quick step of deploying the application services using their pre-built images.

## Key Components

* **`Conductor`**: The highest-level orchestrator. It manages the sequence of deployment phases, including the parallel execution of builds and remote setup. Its behavior is controlled by `ConductorOptions`, which allow phases to be skipped for partial or targeted deployments.

* **`Orchestrator`**: A specialized "worker" component used by the `Conductor`. It handles the practical tasks of deploying services (using the `deployment` package) and managing the event-driven Pub/Sub communication with the deployed `ServiceDirector`.

* **`IAMOrchestrator`**: Another specialized "worker" dedicated to IAM. Its responsibilities are now focused on the identity lifecycle: creating service accounts, managing their project-level roles, and verifying the propagation of both identities and policies.

## Testing Strategy

The orchestration package uses a two-tiered testing strategy to ensure correctness and stability.

### 1. Emulator-Based Integration Test (`orchestrator_integration_test.go`)

This test is designed to be fast and run without a real cloud connection.
* **Purpose**: To verify the core command-and-reply communication loop between the `Orchestrator` and the `ServiceDirector`.
* **Mechanism**: It runs the `ServiceDirector` as an **in-memory goroutine** within the test itself. All communication happens via a **Pub/Sub emulator**. All IAM and service management clients are replaced with mocks, isolating the test to its core responsibility.

### 2. Cloud E2E Integration Test (`orchestrator_dataflow_test.go`)

This is the comprehensive, end-to-end test that validates the entire `Conductor` workflow against real Google Cloud services.
* **Purpose**: To prove that the `Conductor` can successfully orchestrate a full, parallel build-and-deploy of a realistic multi-service application.
* **Mechanism**:
    * It uses a **service account pool** (`TestIAMClient`) to manage identities efficiently and avoid creating new service accounts on every test run.
    * The test **hydrates** its architecture configuration at runtime, injecting the real email addresses of the pooled service accounts before the deployment begins.
    * It builds and deploys a **real `ServiceDirector` application** from a test-specific `main` package, which is configured to use a self-contained Go module. This module requires a stable, versioned release of the main repository, ensuring the build is isolated and reproducible.