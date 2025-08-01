package iam

import (
	"context"
	"fmt"

	"cloud.google.com/go/iam/apiv1/iampb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"github.com/rs/zerolog/log"
	"google.golang.org/api/option"
)

// IAMProjectManager is a dedicated client for managing project-level IAM policies.
// It provides a focused interface for granting roles that apply across the entire GCP project,
// such as the administrative roles required by the ServiceDirector.
type IAMProjectManager struct {
	projectID string
	client    *resourcemanager.ProjectsClient
}

// NewIAMProjectManager creates a new manager for project-level IAM.
func NewIAMProjectManager(ctx context.Context, projectID string, opts ...option.ClientOption) (*IAMProjectManager, error) {
	client, err := resourcemanager.NewProjectsClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resourcemanager client: %w", err)
	}
	return &IAMProjectManager{
		projectID: projectID,
		client:    client,
	}, nil
}

// AddProjectIAMBinding grants a role to a member at the project level.
// It uses the standard "get-modify-set" pattern to ensure that existing bindings are preserved.
// This operation is idempotent; if the member already has the role, no changes are made.
func (m *IAMProjectManager) AddProjectIAMBinding(ctx context.Context, member, role string) error {
	// 1. Get the current IAM policy for the project.
	req := &iampb.GetIamPolicyRequest{
		Resource: fmt.Sprintf("projects/%s", m.projectID),
	}
	policy, err := m.client.GetIamPolicy(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to get project IAM policy: %w", err)
	}

	// 2. Add the new member to the specified role in the policy object.
	var bindingToModify *iampb.Binding
	for _, b := range policy.Bindings {
		if b.Role == role {
			bindingToModify = b
			break
		}
	}

	if bindingToModify == nil {
		// If no binding for this role exists, create a new one.
		bindingToModify = &iampb.Binding{Role: role, Members: []string{member}}
		policy.Bindings = append(policy.Bindings, bindingToModify)
	} else {
		// If the binding exists, check if the member is already present.
		memberExists := false
		for _, m := range bindingToModify.Members {
			if m == member {
				memberExists = true
				break
			}
		}
		if !memberExists {
			bindingToModify.Members = append(bindingToModify.Members, member)
		} else {
			log.Info().Str("member", member).Str("role", role).Msg("Member already has project-level role, no changes needed.")
			return nil
		}
	}

	// 3. Set the updated policy back on the project.
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
func (m *IAMProjectManager) Close() error {
	return m.client.Close()
}
