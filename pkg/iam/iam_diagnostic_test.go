//go:build cloud_integration

package iam_test

import (
	"context"
	"fmt"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDiagnoseCloudBuildPermissions is a diagnostic tool. Its only purpose is to identify
// the default Cloud Build service account for a given project and print a clear
// report of its current IAM roles.
func TestDiagnoseCloudBuildPermissions(t *testing.T) {
	// --- Setup ---
	projectID := checkGCPAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resourceManager, err := deployment.NewResourceManager()
	require.NoError(t, err)

	project, err := resourceManager.GetProject(ctx, projectID)

	projectNumber, err := resourceManager.GetProjectNumber(ctx, projectID)
	require.NoError(t, err)

	// --- Step 2: Construct the Cloud Build SA Email ---
	cloudBuildSA := fmt.Sprintf("%s@cloudbuild.gserviceaccount.com", projectNumber)
	t.Logf("Constructed default Cloud Build service account email: %s", cloudBuildSA)

	// --- Step 3: Get the Full IAM Policy for the Project ---
	// This is more reliable than getting the policy for just the SA, which can sometimes be empty.
	t.Logf("Fetching full IAM policy for project: %s", projectID)
	iamPolicyHandle, err := resourceManager.GetProjectIAMHandle(ctx, project.Name)
	require.NoError(t, err)

	// --- Step 4: Report the Findings ---
	t.Logf("\n\n--- IAM Policy Report for Cloud Build Service Account: %s ---\n", cloudBuildSA)

	memberToFind := "serviceAccount:" + cloudBuildSA
	var rolesFound []string
	for _, binding := range iamPolicyHandle.Bindings {
		for _, member := range binding.Members {
			if member == memberToFind {
				rolesFound = append(rolesFound, binding.Role)
			}
		}
	}

	if len(rolesFound) == 0 {
		t.Log("  No roles found for this service account at the project level.")
	} else {
		t.Log("  Roles found at project level:")
		for _, role := range rolesFound {
			t.Logf("    - %s", role)
		}
	}

	t.Log("\n--- Specific Role Verification ---")
	rolesToVerify := map[string]bool{
		"roles/storage.objectViewer":    false, // For reading source from GCS
		"roles.artifactregistry.writer": false, // For pushing the new image
		"roles/cloudbuild.serviceAgent": false, // Basic build permissions
		"roles/containerregistry.agent": false, // For pulling builder images from gcr.io
	}

	for _, role := range rolesFound {
		if _, ok := rolesToVerify[role]; ok {
			rolesToVerify[role] = true
		}
	}

	var allFound = true
	for role, found := range rolesToVerify {
		if found {
			t.Logf("  [PRESENT ✅] %s", role)
		} else {
			t.Logf("  [MISSING ❌] %s", role)
			allFound = false
		}
	}

	t.Log("\n--- End of Report ---")
	if !allFound {
		t.Log("\nNOTE: At least one expected role was missing. Please grant the MISSING roles to the service account above and re-run the main integration test.")
	} else {
		t.Log("\nNOTE: All commonly required roles appear to be present. If builds still fail, the issue may be related to VPC-SC or other organization policies.")
	}
}
