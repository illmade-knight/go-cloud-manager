# Cloud Orchestration Package

The `orchestration` package provides the top-level coordination logic for deploying and managing an entire microservice architecture on Google Cloud. It integrates the `servicemanager`, `iam`, and `deployment` packages into a cohesive, phase-based workflow.

## Overview

This package introduces a powerful and scalable architectural pattern centered around a **Service Director**. Instead of running all infrastructure and deployment logic from a local machine or a single CI/CD job, this package first deploys a central "Service Director" application into your cloud environment. This director then acts as a remote agent, receiving commands via Pub/Sub to manage the lifecycle of other services and resources.

This decoupled, event-driven approach provides enhanced security, scalability, and operational flexibility.

### The Conductor Workflow

The `Conductor` is the main, user-facing entry point. It executes a multi-phase workflow to stand up an entire environment. Each phase is a distinct, verifiable step, and the `Conductor` is designed to be flexible, allowing you to run the entire sequence or skip steps for partial updates.

The standard, end-to-end workflow is:

1.  **Setup Service Director IAM:** The `IAMOrchestrator` plans and applies all necessary service accounts and project-level IAM roles for the central Service Director application.
2.  **Deploy Service Director:** The `Orchestrator` uses the `deployment` package to build and deploy the Service Director application to Cloud Run. It then waits for the service to become healthy and ready to receive commands.
3.  **Setup Dataflow Resources:** The `Orchestrator` sends an asynchronous `dataflow-setup` command to the Service Director via a Pub/Sub topic. The live Service Director receives this command, uses its internal `ServiceManager` to provision the necessary cloud resources (topics, buckets, etc.), and publishes a "completion" event back to a different Pub/Sub topic. The `Orchestrator` waits until it receives this event.
4.  **Apply Dataflow IAM:** Once the resources for a dataflow exist, the `IAMOrchestrator` is invoked again to apply fine-grained IAM policies, connecting the application services to their newly created resources.
5.  **Deploy Dataflow Services:** Finally, the `Orchestrator` builds and deploys all the application microservices defined within the dataflow.

## Key Components

* **`Conductor`:** The highest-level orchestrator. It manages the sequence of deployment phases and provides the primary `Run()` and `Teardown()` methods. Its behavior is controlled by `ConductorOptions`, which allow phases to be skipped.
* **`Orchestrator`:** The main "worker" component. It handles the practical tasks of deploying services (using the `deployment` package) and managing the event-driven Pub/Sub communication with the deployed Service Director.
* **`IAMOrchestrator`:** A specialized "worker" dedicated to IAM. It uses the `iam` package to handle all service account creation and policy bindings for both the Service Director and the application services.

## Usage

The `Conductor` is the intended entry point for most use cases. You initialize it with your architecture, set the options for the phases you want to run, and execute it.

### Example: Full End-to-End Deployment

This example runs all five phases to deploy an entire architecture from scratch.

```go
package main

import (
	"context"
	"log"

	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

func main() {
	// Assume 'arch' is a loaded and hydrated *servicemanager.MicroserviceArchitecture struct
	var arch *servicemanager.MicroserviceArchitecture 
    // ... load architecture from YAML files and hydrate it ...

	logger := zerolog.New(os.Stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// 1. Define options for a full run.
	conductorOptions := orchestration.ConductorOptions{
		SetupServiceDirectorIAM: true,
		DeployServiceDirector:   true,
		SetupDataflowResources:  true,
		ApplyDataflowIAM:        true,
		DeployDataflowServices:  true,
	}

	// 2. Create the Conductor.
	conductor, err := orchestration.NewConductor(ctx, arch, logger, conductorOptions)
	if err != nil {
		log.Fatalf("Failed to create conductor: %v", err)
	}

    // 3. Defer a full teardown to clean up resources after the run.
    defer func() {
        log.Println("--- Starting Teardown ---")
        if err := conductor.Teardown(context.Background()); err != nil {
            log.Printf("Teardown failed: %v", err)
        }
    }()

	// 4. Execute the entire workflow.
	if err := conductor.Run(ctx); err != nil {
		log.Fatalf("Conductor run failed: %v", err)
	}

	log.Println("Orchestration complete!")
}
```

### Example: Deploying a New Dataflow to an Existing Director

The `Conductor`'s flexibility allows you to skip phases. If the Service Director is already running, you can skip its deployment and provide its URL as an override.

```go
// --- In your main function ---

// The URL of the already-running Service Director
directorURL := "https://my-service-director-xyz.a.run.app"

// 1. Define options to skip the first two phases and provide the override.
partialRunOptions := orchestration.ConductorOptions{
    SetupServiceDirectorIAM: false, // Skip
    DeployServiceDirector:   false, // Skip
    DirectorURLOverride:     directorURL, // Provide existing URL
    SetupDataflowResources:  true,  // Run
    ApplyDataflowIAM:        true,  // Run
    DeployDataflowServices:  true,  // Run
}

// 2. Create and run the Conductor with the new options.
conductor, err := orchestration.NewConductor(ctx, arch, logger, partialRunOptions)
// ...
err = conductor.Run(ctx)
// ...
```