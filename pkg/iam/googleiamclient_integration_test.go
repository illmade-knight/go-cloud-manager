//go:build integration

package iam_test

import (
	"cloud.google.com/go/iam/apiv1/iampb"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-test/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// getProjectNumber is a helper to retrieve the numeric project number for a given project ID.
func getProjectNumber(t *testing.T, ctx context.Context, projectID string) string {
	t.Helper()
	c, err := resourcemanager.NewProjectsClient(ctx)
	require.NoError(t, err)
	defer func() {
		_ = c.Close()
	}()

	req := &resourcemanagerpb.SearchProjectsRequest{
		Query: fmt.Sprintf("id:%s", projectID),
	}
	it := c.SearchProjects(ctx, req)
	proj, err := it.Next()
	require.NoError(t, err)
	require.NotNil(t, proj, "Project should not be nil")

	// The project name is returned as "projects/123456789". We just want the number.
	return proj.Name[len("projects/"):]
}

// TestGrantCloudRunAgentPermissions verifies that the helper function can grant the
// Artifact Registry Reader role to the Cloud Run service agent.
func TestGrantCloudRunAgentPermissions(t *testing.T) {
	// --- Arrange ---
	projectID := auth.CheckGCPAuth(t)
	projectNumber := getProjectNumber(t, context.Background(), projectID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	const location = "europe-west1"
	repoName := fmt.Sprintf("test-repo-for-iam-%d", time.Now().UnixNano())

	// Create a temporary Artifact Registry repository for the test.
	arClient, err := artifactregistry.NewClient(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = arClient.Close() })

	createOp, err := arClient.CreateRepository(ctx, &artifactregistrypb.CreateRepositoryRequest{
		Parent:       fmt.Sprintf("projects/%s/locations/%s", projectID, location),
		RepositoryId: repoName,
		Repository:   &artifactregistrypb.Repository{Format: artifactregistrypb.Repository_DOCKER},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)
	t.Logf("Successfully created test Artifact Registry repository: %s", repoName)
	t.Cleanup(func() {
		t.Logf("Cleaning up Artifact Registry repository: %s", repoName)
		req := &artifactregistrypb.DeleteRepositoryRequest{Name: fmt.Sprintf("projects/%s/locations/%s/repositories/%s", projectID, location, repoName)}
		_, err = arClient.DeleteRepository(context.Background(), req)
		require.NoError(t, err)
	})

	// Create the client needed by the function.
	iamClient, err := iam.NewGoogleIAMClient(ctx, projectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = iamClient.Close() })

	repoConfig := iam.RepoConfig{
		ProjectID:     projectID,
		ProjectNumber: projectNumber,
		Name:          repoName,
		Location:      location,
	}

	// --- Act ---
	t.Log("Granting permissions to Cloud Run agent...")
	modified, err := iam.GrantCloudRunAgentPermissions(ctx, repoConfig, iamClient, logger)
	require.NoError(t, err)
	require.True(t, modified, "Expected the policy to be modified")

	// --- Assert ---
	t.Log("Verifying repository IAM binding...")
	cloudRunAgentMember := fmt.Sprintf("serviceAccount:service-%s@serverless-robot-prod.iam.gserviceaccount.com", projectNumber)
	readerRole := "roles/artifactregistry.reader"
	repoResource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", projectID, location, repoName)

	require.Eventually(t, func() bool {
		policy, err := arClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: repoResource})
		if err != nil {
			t.Logf("Attempt failed to get repository IAM policy: %v", err)
			return false
		}
		for _, binding := range policy.Bindings {
			if binding.Role == readerRole {
				for _, member := range binding.Members {
					if member == cloudRunAgentMember {
						t.Log("✅ Verification successful: Found Cloud Run agent in reader role.")
						return true
					}
				}
			}
		}
		t.Log("Attempt failed: Cloud Run agent not yet found in reader role.")
		return false
	}, 60*time.Second, 5*time.Second, "Artifact Registry IAM policy did not propagate in time")
}
