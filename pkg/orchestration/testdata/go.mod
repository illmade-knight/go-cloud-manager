module testdata
// This module is used for E2E testing of the Conductor.
// It intentionally requires a specific versioned release of the main repository
// from GitHub to test the orchestration flow against a stable dependency.
//
// To test against new library changes, update the version tag below after
// publishing a new release of the main repository.
go 1.23.0

require (
	cloud.google.com/go/pubsub v1.50.0
	github.com/google/uuid v1.6.0
	github.com/illmade-knight/go-cloud-manager v0.2.0-beta
	github.com/rs/zerolog v1.34.0
)
