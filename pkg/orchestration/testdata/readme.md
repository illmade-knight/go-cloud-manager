# Test Data for Orchestration E2E Tests

This directory contains source code for buildable Go applications that are used in the end-to-end (E2E) integration tests for the `Conductor`.

## Go Module Strategy

This `testdata` directory is intentionally configured as a **standalone Go module**, separate from the main repository's module.

Its `go.mod` file **requires a published, versioned release** of the main `github.com/illmade-knight/go-cloud-manager` repository from GitHub.

### Purpose and Trade-offs

The primary goal of the E2E test is to validate the `Conductor`'s orchestration flow (parallel builds, IAM verification, deployment) against a stable, versioned set of its own libraries.

This has an important consequence:
* **This test does NOT validate work-in-progress changes** made to the `pkg/` libraries in your current workspace.
* To test changes made to a library like `pkg/servicedirector`, you must first tag and push a new release of the main repository to GitHub, and then update the version in this directory's `go.mod` file.