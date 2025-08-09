# Service Director Microservice

This package contains the source code for the **Service Director**, the central microservice that acts as a remote, event-driven agent for managing cloud infrastructure. It is the application that gets deployed by the `orchestration` and `deployment` packages and is the primary user of the `servicemanager` package at runtime.

## Overview

The Service Director is the "brain" of the infrastructure management system that runs inside your Google Cloud project. Instead of having a local tool or CI/CD job hold powerful permissions, this responsibility is delegated to the Service Director. The `Orchestrator` sends high-level commands (e.g., "setup dataflow X") to the Director via Pub/Sub. The Director receives these commands and uses its internal instance of the `ServiceManager` to execute the detailed work of creating, deleting, or verifying the resources defined in the architecture.

## Core Responsibilities

* **Receiving Commands:** The Director listens for commands in two ways:

    1.  **Asynchronously (Pub/Sub):** Its primary method of operation is subscribing to a Pub/Sub topic for commands from the `Orchestrator`. This is used for long-running, asynchronous tasks like setting up an entire dataflow.
    2.  **Synchronously (HTTP):** It exposes a simple HTTP API for direct, immediate requests.

* **Managing Resources:** Its core function is to use the `servicemanager` package to perform infrastructure operations based on the loaded architecture configuration. It can set up, tear down, and verify entire dataflows or the whole environment.

* **Reporting Status:** After completing an asynchronous command received from Pub/Sub, the Director publishes a "completion event" back to a reply topic. This signals to the `Orchestrator` that the requested task has finished, allowing the overall workflow to proceed to the next phase.

## HTTP API and Client for Dependent Services

A key feature of the Service Director is its HTTP API, designed to be used by the other microservices that it helps deploy. This allows for a robust "readiness dependency check."

When a new application microservice (e.g., a data processor) starts up, its first action can be to contact the Service Director to confirm that all its required resources (like its input Pub/Sub subscription and output BigQuery table) actually exist. This prevents the service from starting in a broken state.

This package includes a pre-built Go client to make this interaction simple for other services.

### Usage Example (from another microservice)

This is how a separate "tracer-subscriber" service could use the client to verify its resources are ready before starting its main processing loop.

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/illmade-knight/go-cloud-manager/microservice/servicedirector" // Assumed path
    "github.com/rs/zerolog"
)

func main() {
    ctx := context.Background()
    logger := zerolog.New(os.Stdout)
    
    // The URL for the Director is passed to the service as an env var by the Orchestrator
    directorURL := os.Getenv("SERVICE_DIRECTOR_URL")
    if directorURL == "" {
        log.Fatal("SERVICE_DIRECTOR_URL is not set")
    }

    // The service knows its own name and the dataflow it belongs to
    myServiceName := os.Getenv("SERVICE_NAME")
    myDataflowName := os.Getenv("DATAFLOW_NAME")

    // 1. Create a new client for the Service Director
    directorClient, err := servicedirector.NewClient(directorURL, logger)
    if err != nil {
        log.Fatalf("Could not create director client: %v", err)
    }

    // 2. Call the VerifyDataflow endpoint before starting work
    log.Println("Verifying dataflow resources with Service Director...")
    err = directorClient.VerifyDataflow(ctx, myDataflowName, myServiceName)
    if err != nil {
        log.Fatalf("Dataflow verification failed! Resources may be missing: %v", err)
    }
    log.Println("Dataflow resources are ready. Starting main application logic.")

    // ... proceed with application startup ...
}
```

## Configuration

The Service Director is configured at runtime via environment variables.

* `PROJECT_ID`: The Google Cloud Project ID.
* `PORT`: (Provided automatically by Cloud Run) The port for the HTTP health check and API server.
* `SD_COMMAND_TOPIC`: The ID of the Pub/Sub topic where it listens for commands from the `Orchestrator`.
* `SD_COMMAND_SUBSCRIPTION`: The ID of the Pub/Sub subscription tied to the command topic.
* `SD_COMPLETION_TOPIC`: The ID of the Pub/Sub topic where it publishes completion events.