package iam

import (
	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/iam/apiv1/iampb"
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
)

func GrantCloudRunAgentPermissions(ctx context.Context, rc RepoConfig, iamClient IAMClient) (bool, error) {
	// --- 2. Define the member and role needed ---
	cloudRunAgentEmail := fmt.Sprintf("service-%s@serverless-robot-prod.iam.gserviceaccount.com", rc.ProjectNumber)

	member := "serviceAccount:" + cloudRunAgentEmail
	role := "roles/artifactregistry.reader"
	repoResource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", rc.ProjectID, rc.Location, rc.Name)

	log.Info().Msgf("Ensuring member '%s' has role '%s' on repository '%s'", member, role, rc.Name)
	arClient, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return false, err
	}
	defer arClient.Close()

	policy, err := arClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: repoResource})
	if err != nil {
		return false, err
	}

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
	}

	// --- 4. Add the permission and wait ONLY if it's missing ---
	if !permissionExists {
		log.Warn().Msgf("Permission NOT FOUND. Granting role '%s' to member '%s'.", role, member)

		err = iamClient.AddArtifactRegistryRepositoryIAMBinding(ctx, rc.Location, rc.Name, role, member)
		if err != nil {
			return false, err
		}
		return true, nil
	} else {
		log.Info().Msg("✅ Permission already exists. No action needed.")
	}
	return false, nil
}
