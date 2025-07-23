package orchestration_test

import (
	"cloud.google.com/go/pubsub"
	"context"
	"os"
	"testing"
)

// CheckGCPAuth is a helper that fails fast if the test is not configured to run
// with valid Application Default Credentials (ADC) that can invoke Cloud Run.
func CheckGCPAuth(t *testing.T) string {
	t.Helper()
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		t.Skip("Skipping real integration test: GCP_PROJECT_ID environment variable is not set")
	}
	ctx := context.Background()

	// 1. Check basic connectivity and authentication for resource management.
	_, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		t.Fatalf(`
		---------------------------------------------------------------------
		GCP AUTHENTICATION FAILED!
		---------------------------------------------------------------------
		Could not create a Google Cloud client. This is likely due to
		expired or missing Application Default Credentials (ADC).

		To fix this, please run 'gcloud auth application-default login'.

		Original Error: %v
		---------------------------------------------------------------------
		`, err)
	}

	//// Log the principal associated with the Application Default Credentials.
	//creds, err := google.FindDefaultCredentials(ctx)
	//if err == nil {
	//	// Attempt to unmarshal the JSON to find the principal.
	//	var credsMap map[string]interface{}
	//	if json.Unmarshal(creds.JSON, &credsMap) == nil {
	//		if clientEmail, ok := credsMap["client_email"]; ok {
	//			t.Logf("--- Using GCP Service Account: %s", clientEmail)
	//		} else {
	//			// For user credentials, the file path is the most reliable identifier.
	//			t.Logf("--- Using GCP User Credentials from file: %s", os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	//		}
	//	}
	//}
	//
	// TODO introduce this check if we setup a dedicated test runner service account
	//// 2. Check for the specific ability to create ID tokens, which is needed to invoke Cloud Run.
	//// This check validates the credential type without making a network call.
	//_, err = idtoken.NewTokenSource(ctx, "https://example.com")
	//if err != nil && strings.Contains(err.Error(), "unsupported credentials type") {
	//	// This is the specific error the user is seeing. Provide a detailed, actionable fix.
	//	t.Fatalf(`
	//	---------------------------------------------------------------------
	//	GCP INVOCATION AUTHENTICATION FAILED!
	//	---------------------------------------------------------------------
	//	The test failed because your Application Default Credentials (ADC)
	//	are user credentials, which cannot be used by this client library to
	//	invoke secure Cloud Run services directly.
	//
	//	To fix this, the user running the test needs the permission to invoke
	//	Cloud Run services.
	//
	//	SOLUTION: Grant your user the 'Cloud Run Invoker' role on the project.
	//	   1. Find your user email by running:
	//	      gcloud auth list --filter=status:ACTIVE --format="value(account)"
	//	   2. Grant the role by running (replace [YOUR_EMAIL] and [YOUR_PROJECT]):
	//	      gcloud projects add-iam-policy-binding %s --member="user:[YOUR_EMAIL]" --role="roles/run.invoker"
	//
	//	After granting the permission, you may need to refresh your credentials:
	//	gcloud auth application-default login
	//
	//	Original Error: %v
	//	---------------------------------------------------------------------
	//	`, projectID, err)
	//} else if err != nil {
	//	// A different, unexpected token-related error occurred.
	//	t.Fatalf("Failed to create an ID token source, please check your GCP auth: %v", err)
	//}
	//
	return projectID
}
