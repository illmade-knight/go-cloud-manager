package iam

import (
	"cloud.google.com/go/iam"
	"context"
	"errors"
	"fmt"

	// Core IAM Admin Client for service account management (create, get, delete)
	iamadmin "cloud.google.com/go/iam/admin/apiv1"
	// Protobuf types specific to the IAM Admin API (e.g., ServiceAccount, CreateServiceAccountRequest)
	"cloud.google.com/go/iam/admin/apiv1/adminpb"

	"cloud.google.com/go/iam/apiv1/iampb"
	"github.com/rs/zerolog"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ServiceAccountManager provides methods to manage service accounts (e.g., create, delete)
// and the IAM policies bound directly to them.
type ServiceAccountManager struct {
	adminClient *iamadmin.IamClient
	projectID   string
	logger      zerolog.Logger
}

// NewServiceAccountManager creates and returns a new ServiceAccountManager.
func NewServiceAccountManager(ctx context.Context, projectID string, logger zerolog.Logger) (*ServiceAccountManager, error) {
	client, err := iamadmin.NewIamClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM admin client: %w", err)
	}
	return &ServiceAccountManager{
		adminClient: client,
		projectID:   projectID,
		logger:      logger.With().Str("component", "ServiceAccountManager").Logger(),
	}, nil
}

// Close closes the underlying client connection.
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

// GetServiceAccount retrieves a service account by its ID (the part of the email before the '@').
func (sm *ServiceAccountManager) GetServiceAccount(ctx context.Context, accountID string) (*adminpb.ServiceAccount, error) {
	serviceAccountEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, sm.projectID)
	resourceName := sm.getServiceAccountName(serviceAccountEmail)

	req := &adminpb.GetServiceAccountRequest{
		Name: resourceName,
	}

	sa, err := sm.adminClient.GetServiceAccount(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get service account %q: %w", serviceAccountEmail, err)
	}
	sm.logger.Debug().Str("email", sa.GetEmail()).Msg("Found service account.")
	return sa, nil
}

// CreateServiceAccountIfNotExists creates a service account if it doesn't already exist.
// Returns the service account's email, a boolean indicating if it was newly created, and an error.
func (sm *ServiceAccountManager) CreateServiceAccountIfNotExists(ctx context.Context, accountID, displayName, description string) (string, bool, error) {
	serviceAccountEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, sm.projectID)
	// First, try to get the service account.
	_, err := sm.GetServiceAccount(ctx, accountID)
	if err == nil {
		// Service account already exists.
		sm.logger.Info().Str("email", serviceAccountEmail).Msg("Service account already exists.")
		return serviceAccountEmail, false, nil
	}

	// If the error indicates "Not Found", proceed with creation.
	if status.Code(err) == codes.NotFound {
		createReq := &adminpb.CreateServiceAccountRequest{
			Name: fmt.Sprintf("projects/%s", sm.projectID),
			ServiceAccount: &adminpb.ServiceAccount{
				DisplayName: displayName,
				Description: description,
			},
			AccountId: accountID,
		}

		sa, createErr := sm.adminClient.CreateServiceAccount(ctx, createReq)
		if createErr != nil {
			return "", false, fmt.Errorf("failed to create service account %q: %w", accountID, createErr)
		}
		sm.logger.Info().Str("email", sa.GetEmail()).Msg("Successfully created service account.")
		return sa.GetEmail(), true, nil
	}
	// Any other error is unexpected.
	return "", false, fmt.Errorf("unexpected error when checking service account %q: %w", accountID, err)
}

// GetServiceAccountIAMPolicy retrieves the IAM policy for a given service account.
func (sm *ServiceAccountManager) GetServiceAccountIAMPolicy(ctx context.Context, serviceAccountEmail string) (*iam.Policy, error) {
	resourceName := sm.getServiceAccountName(serviceAccountEmail)
	req := &iampb.GetIamPolicyRequest{
		Resource: resourceName,
	}

	policy, err := sm.adminClient.GetIamPolicy(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get IAM policy for %q: %w", serviceAccountEmail, err)
	}

	return policy, nil
}

// SetServiceAccountIAMPolicy updates the IAM policy for a given service account.
func (sm *ServiceAccountManager) SetServiceAccountIAMPolicy(ctx context.Context, serviceAccountEmail string, policy *iam.Policy) error {
	resourceName := sm.getServiceAccountName(serviceAccountEmail)

	// As per Google's source, the *IamClient.SetIamPolicy method takes a *SetIamPolicyRequest,
	// which contains *iam.Policy.
	req := &iamadmin.SetIamPolicyRequest{
		Resource: resourceName,
		Policy:   policy, // This is the *iam.Policy type
	}

	_, err := sm.adminClient.SetIamPolicy(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to set IAM policy for %q: %w", serviceAccountEmail, err)
	}
	return nil
}

// ListServiceAccounts lists all service accounts in the project.
func (sm *ServiceAccountManager) ListServiceAccounts(ctx context.Context) ([]*adminpb.ServiceAccount, error) {
	listReq := &adminpb.ListServiceAccountsRequest{
		Name: fmt.Sprintf("projects/%s", sm.projectID),
	}
	it := sm.adminClient.ListServiceAccounts(ctx, listReq)

	var serviceAccounts []*adminpb.ServiceAccount
	for {
		sa, err := it.Next()
		if errors.Is(err, iterator.Done) {
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
func (sm *ServiceAccountManager) DeleteServiceAccount(ctx context.Context, serviceAccountEmail string) error {
	resourceName := sm.getServiceAccountName(serviceAccountEmail)
	req := &adminpb.DeleteServiceAccountRequest{
		Name: resourceName,
	}
	err := sm.adminClient.DeleteServiceAccount(ctx, req)
	if err != nil {
		// It's not an error if the account is already gone.
		if status.Code(err) == codes.NotFound {
			sm.logger.Info().Str("email", serviceAccountEmail).Msg("Service account not found, skipping deletion.")
			return nil
		}
		return fmt.Errorf("failed to delete service account %q: %w", serviceAccountEmail, err)
	}
	sm.logger.Info().Str("email", serviceAccountEmail).Msg("Successfully deleted service account.")
	return nil
}
