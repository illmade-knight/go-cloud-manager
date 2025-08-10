package iam

import (
	"context"
	"fmt"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/iam/apiv1/iampb"
	"github.com/rs/zerolog"
)

type RepoConfig struct {
	ProjectID     string
	ProjectNumber string
	Location      string
	Name          string
}

// GrantCloudRunAgentPermissions ensures that the Google-managed Cloud Run service agent
// has reader permissions on a specific Artifact Registry repository. This is a necessary
// permission for Cloud Run services to be able to pull container images from that repository.
//
// This function is idempotent. If the permission already exists, it does nothing.
//
// Returns:
//   - bool: True if the IAM policy was modified, false otherwise.
//   - error: An error if any API call fails.
func GrantCloudRunAgentPermissions(ctx context.Context, rc RepoConfig, iamClient IAMClient, logger zerolog.Logger) (bool, error) {
	// The Cloud Run service agent is a Google-managed service account with a predictable email format.
	cloudRunAgentEmail := fmt.Sprintf("service-%s@serverless-robot-prod.iam.gserviceaccount.com", rc.ProjectNumber)
	member := "serviceAccount:" + cloudRunAgentEmail
	role := "roles/artifactregistry.reader"
	repoResource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", rc.ProjectID, rc.Location, rc.Name)

	log := logger.With().Str("component", "IAMHelper").Logger()
	log.Info().Str("member", member).Str("role", role).Str("repository", rc.Name).Msg("Ensuring Cloud Run agent permissions...")

	arClient, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to create artifactregistry client: %w", err)
	}
	defer func() {
		_ = arClient.Close()
	}()

	// Get the current IAM policy for the repository.
	getPolicyReq := &iampb.GetIamPolicyRequest{Resource: repoResource}
	policy, err := arClient.GetIamPolicy(ctx, getPolicyReq)
	if err != nil {
		return false, fmt.Errorf("failed to get IAM policy for repository '%s': %w", rc.Name, err)
	}

	// Check if the binding already exists.
	permissionExists := false
	for _, binding := range policy.Bindings {
		if binding.Role == role {
			for _, m := range binding.Members {
				if m == member {
					permissionExists = true
					break
				}
			}
		}
		if permissionExists {
			break
		}
	}

	// If the permission already exists, we're done.
	if permissionExists {
		log.Info().Msg("✅ Permission already exists. No action needed.")
		return false, nil
	}

	// If the permission is missing, add it.
	log.Warn().Msg("Permission NOT FOUND. Granting reader role to Cloud Run service agent.")
	err = iamClient.AddArtifactRegistryRepositoryIAMBinding(ctx, rc.Location, rc.Name, role, member)
	if err != nil {
		return false, err // The underlying client will return a formatted error.
	}

	return true, nil
}
