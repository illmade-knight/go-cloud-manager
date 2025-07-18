package iam

import (
	"cloud.google.com/go/pubsub"
	"context"
	"os"
	"testing"
)

// CheckGCPAuth is a helper that fails fast if the test is not configured to run.
func CheckGCPAuth(t *testing.T) string {
	t.Helper()
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		t.Skip("Skipping real integration test: GCP_PROJECT_ID environment variable is not set")
	}
	// A simple adminClient creation is enough to check basic auth config
	// without performing a full API call like listing resources.
	_, err := pubsub.NewClient(context.Background(), projectID)
	if err != nil {
		t.Fatalf(`
		---------------------------------------------------------------------
		GCP AUTHENTICATION FAILED!
		---------------------------------------------------------------------
		Could not create a Google Cloud adminClient. This is likely due to
		expired or missing Application Default Credentials (ADC).

		To fix this, please run 'gcloud auth application-default login'.

		Original Error: %v
		---------------------------------------------------------------------
		`, err)
	}
	return projectID
}
