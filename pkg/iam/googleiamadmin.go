package iam

import (
	"cloud.google.com/go/iam"
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
	"strings"

	// 1. Core IAM Admin Client for service account management (create, get, delete)
	iamadmin "cloud.google.com/go/iam/admin/apiv1"
	// 2. Protobuf types specific to the IAM Admin API (e.g., ServiceAccount, CreateServiceAccountRequest)
	"cloud.google.com/go/iam/admin/apiv1/adminpb"

	"cloud.google.com/go/iam/apiv1/iampb" // Aliased as iampb by convention

	"google.golang.org/api/iterator" // For iterating through lists of resources
)

// ServiceAccountManager provides methods to manage service accounts and their IAM policies.
type ServiceAccountManager struct {
	adminClient *iamadmin.IamClient // The IAM Admin API adminClient
	projectID   string              // The Google Cloud Project ID
	ctx         context.Context     // Context for API calls
}

// NewServiceAccountManager creates and returns a new ServiceAccountManager.
// It initializes the IAM Admin adminClient.
func NewServiceAccountManager(ctx context.Context, projectID string) (*ServiceAccountManager, error) {
	client, err := iamadmin.NewIamClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM adminClient: %w", err)
	}
	return &ServiceAccountManager{
		adminClient: client,
		projectID:   projectID,
		ctx:         ctx,
	}, nil
}

// Close closes the underlying adminClient connection.
func (sm *ServiceAccountManager) Close() error {
	if sm.adminClient != nil {
		return sm.adminClient.Close()
	}
	return nil
}

// getServiceAccountName creates the full resource name for a service account.
// Example: projects/your-project-id/serviceAccounts/my-sa@your-project-id.iam.gserviceaccount.com
func (sm *ServiceAccountManager) getServiceAccountName(accountEmail string) string {
	return fmt.Sprintf("projects/%s/serviceAccounts/%s", sm.projectID, accountEmail)
}

// GetServiceAccount retrieves a service account by its ID.
// It returns the full ServiceAccount object or an error if not found or another issue occurs.
func (sm *ServiceAccountManager) GetServiceAccount(accountID string) (*adminpb.ServiceAccount, error) {
	serviceAccountEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, sm.projectID)
	resourceName := sm.getServiceAccountName(serviceAccountEmail)

	req := &adminpb.GetServiceAccountRequest{ // Type from adminpb
		Name: resourceName,
	}

	sa, err := sm.adminClient.GetServiceAccount(sm.ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get service account %q: %w", serviceAccountEmail, err)
	}
	log.Printf("Found service account: Display Name: %s, Email: %s", sa.GetDisplayName(), sa.GetEmail())
	return sa, nil
}

// CreateServiceAccountIfNotExists creates a service account if it doesn't already exist.
// Returns the service account's email, a boolean indicating if it was newly created, and an error.
func (sm *ServiceAccountManager) CreateServiceAccountIfNotExists(accountID, displayName, description string) (string, bool, error) {
	// First, try to get the service account
	_, err := sm.GetServiceAccount(accountID)
	if err == nil {
		// Service account already exists
		serviceAccountEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, sm.projectID)
		log.Printf("Service account %s already exists.", serviceAccountEmail)
		return serviceAccountEmail, false, nil
	}

	// If the error indicates "Not Found", proceed with creation
	if strings.Contains(err.Error(), "NotFound") {
		createReq := &adminpb.CreateServiceAccountRequest{ // Type from adminpb
			Name: fmt.Sprintf("projects/%s", sm.projectID), // Parent resource for creation
			ServiceAccount: &adminpb.ServiceAccount{ // Type from adminpb
				DisplayName: displayName,
				Description: description,
			},
			AccountId: accountID, // The ID portion of the email (e.g., "my-worker")
		}

		sa, err := sm.adminClient.CreateServiceAccount(sm.ctx, createReq)
		if err != nil {
			return "", false, fmt.Errorf("failed to create service account %q: %w", accountID, err)
		}
		log.Printf("Successfully created service account: %s (%s)", sa.GetDisplayName(), sa.GetEmail())
		return sa.GetEmail(), true, nil
	}
	// Other error than "NotFound" occurred
	return "", false, fmt.Errorf("unexpected error when checking service account %q: %w", accountID, err)
}

// GetServiceAccountIAMPolicy retrieves the IAM policy for a given service account.
// This now correctly returns *iampb.Policy.
// GetServiceAccountIAMPolicy retrieves the IAM policy for a given service account.
// This function will now accurately reflect the types and aliases from Google's source.
func (sm *ServiceAccountManager) GetServiceAccountIAMPolicy(serviceAccountEmail string) (*iam.Policy, error) {
	resourceName := sm.getServiceAccountName(serviceAccountEmail)

	// The request struct (GetIamPolicyRequest) is from the iampb package.
	req := &iampb.GetIamPolicyRequest{
		Resource: resourceName,
	}

	// As per Google's source, sm.adminClient.GetIamPolicy returns *iam.Policy.
	// This variable 'policy' will correctly be of type *iam.Policy.
	policy, err := sm.adminClient.GetIamPolicy(sm.ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get IAM policy for %q: %w", serviceAccountEmail, err)
	}

	return policy, nil
}

// SetServiceAccountIAMPolicy updates the IAM policy for a given service account.
// This will also stick to Google's aliases.
func (sm *ServiceAccountManager) SetServiceAccountIAMPolicy(serviceAccountEmail string, policy *iam.Policy) error {
	resourceName := sm.getServiceAccountName(serviceAccountEmail)

	// As per Google's source, the *IamClient.SetIamPolicy method takes a *SetIamPolicyRequest,
	// which contains *iam.Policy.
	req := &iamadmin.SetIamPolicyRequest{
		Resource: resourceName,
		Policy:   policy, // This is the *iam.Policy type
	}

	_, err := sm.adminClient.SetIamPolicy(sm.ctx, req)
	if err != nil {
		return fmt.Errorf("failed to set IAM policy for %q: %w", serviceAccountEmail, err)
	}
	return nil
}

// EnsureRolesOnServiceAccount verifies and adds missing roles to a service account's policy.
// It modifies the policy IN PLACE and then applies it using SetServiceAccountIAMPolicy.
// This adds roles *to the service account itself*, meaning this service account
// will be a *member* of the specified roles in its own policy.
// And the EnsureRolesOnServiceAccount will now also use *iam.Policy and *iam.Binding
// from the 'iam' package.
func (sm *ServiceAccountManager) EnsureRolesOnServiceAccount(serviceAccountEmail string, requiredRoles []string) error {
	policy, err := sm.GetServiceAccountIAMPolicy(serviceAccountEmail)
	if err != nil {
		return fmt.Errorf("could not get policy to ensure roles: %w", err)
	}

	member := "serviceAccount:" + serviceAccountEmail
	modified := false

	for _, reqRole := range requiredRoles {
		foundBinding := false
		// policy.InternalProto.Bindings accesses the underlying *iampb.Policy's Bindings
		// which are []*iampb.Binding
		for _, binding := range policy.InternalProto.Bindings {
			if binding.Role == reqRole {
				foundMember := false
				for _, m := range binding.Members {
					if m == member {
						foundMember = true
						break
					}
				}
				if !foundMember {
					binding.Members = append(binding.Members, member)
					log.Printf("Added member %q to role %q in existing binding for service account %s.", member, reqRole, serviceAccountEmail)
					modified = true
				}
				foundBinding = true
				break
			}
		}

		if !foundBinding {
			// When adding a new binding, we need to create an iampb.Binding directly
			// because the iam.Policy.Add method isn't for adding new Bindings,
			// it's for adding members to existing roles.
			if policy.InternalProto == nil {
				policy.InternalProto = &iampb.Policy{}
			}
			policy.InternalProto.Bindings = append(policy.InternalProto.Bindings, &iampb.Binding{
				Role:    reqRole,
				Members: []string{member},
			})
			log.Printf("Created new binding for role %q with member %q for service account %s.", reqRole, member, serviceAccountEmail)
			modified = true
		}
	}

	if modified {
		err = sm.SetServiceAccountIAMPolicy(serviceAccountEmail, policy)
		if err != nil {
			return fmt.Errorf("failed to update IAM policy after adding roles for %s: %w", serviceAccountEmail, err)
		}
		log.Printf("Successfully updated IAM policy for service account %s with missing roles.", serviceAccountEmail)
	} else {
		log.Printf("All required roles already present for service account %s. No changes made.", serviceAccountEmail)
	}

	return nil
}

// ListServiceAccounts lists all service accounts in the project.
func (sm *ServiceAccountManager) ListServiceAccounts() ([]*adminpb.ServiceAccount, error) {
	listReq := &adminpb.ListServiceAccountsRequest{ // Type from adminpb
		Name: fmt.Sprintf("projects/%s", sm.projectID), // Parent resource for listing
	}
	it := sm.adminClient.ListServiceAccounts(sm.ctx, listReq)

	var serviceAccounts []*adminpb.ServiceAccount // Type for elements from adminpb
	for {
		sa, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate service accounts: %w", err)
		}
		serviceAccounts = append(serviceAccounts, sa)
	}
	return serviceAccounts, nil
}

// DeleteServiceAccount deletes a service account.
func (sm *ServiceAccountManager) DeleteServiceAccount(serviceAccountEmail string) error {
	resourceName := sm.getServiceAccountName(serviceAccountEmail)
	req := &adminpb.DeleteServiceAccountRequest{ // Type from adminpb
		Name: resourceName,
	}
	err := sm.adminClient.DeleteServiceAccount(sm.ctx, req)
	if err != nil {
		return fmt.Errorf("failed to delete service account %q: %w", serviceAccountEmail, err)
	}
	log.Printf("Successfully deleted service account: %q", serviceAccountEmail)
	return nil
}
