package iam

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/iam/apiv1/iampb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"github.com/rs/zerolog/log"
	"google.golang.org/api/option"
)

// GoogleIAMProjectClient is a dedicated client for managing project-level IAM policies.
// It provides a focused interface for granting roles that apply across the entire GCP project,
// such as the administrative roles required by the ServiceDirector.
type GoogleIAMProjectClient struct {
	projectID string
	client    *resourcemanager.ProjectsClient
}

// NewGoogleIAMProjectClient creates a new manager for project-level IAM.
func NewGoogleIAMProjectClient(ctx context.Context, projectID string, opts ...option.ClientOption) (*GoogleIAMProjectClient, error) {
	client, err := resourcemanager.NewProjectsClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resourcemanager client: %w", err)
	}
	return &GoogleIAMProjectClient{
		projectID: projectID,
		client:    client,
	}, nil
}

// GrantFirestoreProjectRole grants a Firestore/Datastore role to a member at the project level.
// It includes a strict check to ensure only 'roles/datastore.*' roles can be granted,
// preventing accidental escalation of privileges for other services.
func (m *GoogleIAMProjectClient) GrantFirestoreProjectRole(ctx context.Context, member, role string) error {
	// SECURITY_CHECK: As requested, we ensure that this powerful project-level grant
	// can only be used for its intended purpose of managing Firestore/Datastore access.
	if !strings.HasPrefix(role, "roles/datastore.") {
		return fmt.Errorf("invalid request: this function can only grant project-level roles for Firestore/Datastore (e.g., 'roles/datastore.user'), not '%s'", role)
	}
	return m.AddProjectIAMBinding(ctx, member, role)
}

// CheckProjectIAMBinding verifies if a member has a specific role at the project level.
func (m *GoogleIAMProjectClient) CheckProjectIAMBinding(ctx context.Context, member, role string) (bool, error) {
	req := &iampb.GetIamPolicyRequest{
		Resource: fmt.Sprintf("projects/%s", m.projectID),
	}
	policy, err := m.client.GetIamPolicy(ctx, req)
	if err != nil {
		return false, fmt.Errorf("failed to get project IAM policy for verification: %w", err)
	}

	// Iterate through the policy bindings to find the role.
	for _, binding := range policy.Bindings {
		if binding.Role == role {
			// Once the role is found, check if the member is in the list.
			for _, m := range binding.Members {
				if m == member {
					return true, nil // Member has the role.
				}
			}
		}
	}

	return false, nil // Member does not have the role.
}

// AddProjectIAMBinding grants a role to a member at the project level.
// It uses the standard "get-modify-set" pattern to ensure that existing bindings are preserved.
func (m *GoogleIAMProjectClient) AddProjectIAMBinding(ctx context.Context, member, role string) error {
	// 1. Get the current IAM policy for the project.
	req := &iampb.GetIamPolicyRequest{
		Resource: fmt.Sprintf("projects/%s", m.projectID),
	}
	policy, err := m.client.GetIamPolicy(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to get project IAM policy: %w", err)
	}

	// 2. Find the binding for the specified role, or create it if it doesn't exist.
	var bindingToModify *iampb.Binding
	for _, b := range policy.Bindings {
		if b.Role == role {
			bindingToModify = b
			break
		}
	}
	if bindingToModify == nil {
		bindingToModify = &iampb.Binding{Role: role}
		policy.Bindings = append(policy.Bindings, bindingToModify)
	}

	memberExists := false
	for _, m := range bindingToModify.Members {
		if m == member {
			memberExists = true
			break
		}
	}

	if memberExists {
		log.Info().Str("member", member).Str("role", role).Msg("Member already has project-level role, no changes needed.")
		// Even if we think it exists, we continue to the SetIamPolicy call to be certain.
	} else {
		bindingToModify.Members = append(bindingToModify.Members, member)
	}

	// 4. Set the updated policy back on the project.
	setReq := &iampb.SetIamPolicyRequest{
		Resource: fmt.Sprintf("projects/%s", m.projectID),
		Policy:   policy,
	}
	_, err = m.client.SetIamPolicy(ctx, setReq)
	if err != nil {
		return fmt.Errorf("failed to set project IAM policy: %w", err)
	}

	log.Info().Str("member", member).Str("role", role).Msg("Successfully granted project-level IAM role.")
	return nil
}

// Close closes the underlying client connection.
func (m *GoogleIAMProjectClient) Close() error {
	return m.client.Close()
}
