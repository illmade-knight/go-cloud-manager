# Go Cloud Manager

This repository contains a comprehensive, Go-based framework for declaratively managing, deploying, and securing complex microservice architectures on Google Cloud Platform. It treats your entire cloud environment as code, integrating resource provisioning, IAM policy management, and application deployment into a cohesive, automated workflow.

## Architecture & Package Organization

The project is organized into several distinct packages, each with a clear responsibility, forming a layered and decoupled architecture. For more detailed information, please see the `README.md` file within each package's directory.

### `/pkg/servicemanager` - Resource Lifecycle Management

This is the foundational layer responsible for managing the lifecycle (create, update, verify, teardown) of core cloud infrastructure resources like Pub/Sub topics, GCS buckets, and BigQuery datasets. Its behavior is driven by YAML files that define the system's desired state.

### `/pkg/iam` - Permissions & Security

This package is the security and permissions layer, responsible for automating all Identity and Access Management (IAM) policies. Its key feature is a `RolePlanner` that intelligently infers required permissions from the architecture definition, applying the principle of least privilege with minimal configuration. It manages service accounts and applies policies to a wide variety of GCP resources.

### `/pkg/deployment` - Build & Deploy Engine

This package acts as the CI/CD engine for the system. It takes application source code, builds it into a container using Google Cloud Build (without requiring a Dockerfile), pushes it to Artifact Registry, and deploys it as a service on Cloud Run.

### `/pkg/orchestration` - Top-Level Workflow Coordination

This is the highest-level package that integrates all other components to execute a complete, end-to-end deployment. It implements the Service Director pattern by using the `/iam` and `/deployment` packages to set up the `/servicedirector` microservice first. It then communicates with the live Service Director via event-driven Pub/Sub messages to orchestrate the setup of application resources and services.


### `/microservice/servicedirector` - Remote Management Microservice

This package contains the source code for the Service Director, a central microservice that acts as a remote agent for managing cloud infrastructure. It is deployed into the cloud environment and receives commands via Pub/Sub or HTTP to execute resource management tasks using the `/servicemanager` package. It also provides an API client that other deployed microservices can use to verify their own resource dependencies are in place before starting up.


### AI AGENT CODED
the majority of code is created using Gemini
the code is tested, human directed and reviewed 

we make an effort to minimise imports and dependencies 
