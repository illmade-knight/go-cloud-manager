package iam

import (
	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/iam"
	"cloud.google.com/go/iam/admin/apiv1"
	"cloud.google.com/go/iam/admin/apiv1/adminpb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/pubsub"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/storage"
	"context"
	"errors"
	"fmt"
	"github.com/rs/zerolog/log"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

// --- (Structs, NewGoogleIAMClient, and other functions are included for completeness) ---

type RepoConfig struct {
	ProjectID     string
	ProjectNumber string
	Name          string
	Location      string
}
type iamHandle interface {
	Policy(ctx context.Context) (*iam.Policy, error)
	SetPolicy(ctx context.Context, p *iam.Policy) error
}
type secretIAMHandle struct {
	client     *secretmanager.Client
	resourceID string
}

func (h *secretIAMHandle) Policy(ctx context.Context) (*iam.Policy, error) {
	policy, err := h.client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{
		Resource: h.resourceID,
	})
	if err != nil {
		return nil, err
	}
	return &iam.Policy{InternalProto: policy}, nil
}
func (h *secretIAMHandle) SetPolicy(ctx context.Context, p *iam.Policy) error {
	_, err := h.client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: h.resourceID,
		Policy:   p.InternalProto,
	})
	return err
}

type GoogleIAMClient struct {
	projectID      string
	iamAdminClient *admin.IamClient
	pubsubClient   *pubsub.Client
	storageClient  *storage.Client
	bigqueryClient *bigquery.Client
	secretsClient  *secretmanager.Client
	runService     *run.Service
}

// NewGoogleIAMClient creates a new, fully initialized GoogleIAMClient.
func NewGoogleIAMClient(ctx context.Context, projectID string, opts ...option.ClientOption) (*GoogleIAMClient, error) {
	adminClient, err := admin.NewIamClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM admin client: %w", err)
	}
	psClient, err := pubsub.NewClient(ctx, projectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub client: %w", err)
	}
	gcsClient, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage client: %w", err)
	}
	bqClient, err := bigquery.NewClient(ctx, projectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create bigquery client: %w", err)
	}
	secretsClient, err := secretmanager.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create secretmanager client: %w", err)
	}
	runService, err := run.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create run service: %w", err)
	}
	return &GoogleIAMClient{
		projectID:      projectID,
		iamAdminClient: adminClient,
		pubsubClient:   psClient,
		storageClient:  gcsClient,
		bigqueryClient: bqClient,
		secretsClient:  secretsClient,
		runService:     runService,
	}, nil
}

// AddResourceIAMBinding adds a member to a role on a specific cloud resource.
// It acts as a router, dispatching the request to the appropriate handler based on the resource type.
func (c *GoogleIAMClient) AddResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error {
	log.Info().
		Str("resourceType", binding.ResourceType).
		Str("resourceID", binding.ResourceID).
		Str("role", binding.Role).
		Str("member", member).
		Msg("GoogleIAMClient: Attempting to add resource IAM binding")

	switch binding.ResourceType {
	case "bigquery_dataset":
		return c.addBigQueryDatasetIAMBinding(ctx, binding.ResourceID, binding.Role, member)
	case "pubsub_topic":
		return addStandardIAMBinding(ctx, c.pubsubClient.Topic(binding.ResourceID).IAM(), binding.Role, member)
	case "pubsub_subscription":
		return addStandardIAMBinding(ctx, c.pubsubClient.Subscription(binding.ResourceID).IAM(), binding.Role, member)
	case "gcs_bucket":
		return addStandardIAMBinding(ctx, c.storageClient.Bucket(binding.ResourceID).IAM(), binding.Role, member)
	case "secret":
		handle := &secretIAMHandle{
			client:     c.secretsClient,
			resourceID: fmt.Sprintf("projects/%s/secrets/%s", c.projectID, binding.ResourceID),
		}
		return addStandardIAMBinding(ctx, handle, binding.Role, member)
	case "cloudrun_service":
		return c.addCloudRunServiceIAMBinding(ctx, binding.ResourceLocation, binding.ResourceID, binding.Role, member)
	default:
		return fmt.Errorf("unsupported resource type for IAM binding: %s", binding.ResourceType)
	}
}

// RemoveResourceIAMBinding removes a member from a role on a specific cloud resource.
func (c *GoogleIAMClient) RemoveResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error {
	log.Info().
		Str("resourceType", binding.ResourceType).
		Str("resourceID", binding.ResourceID).
		Str("role", binding.Role).
		Str("member", member).
		Msg("GoogleIAMClient: Attempting to remove resource IAM binding")

	switch binding.ResourceType {
	case "bigquery_dataset":
		return c.removeBigQueryDatasetIAMBinding(ctx, binding.ResourceID, binding.Role, member)
	case "pubsub_topic":
		return removeStandardIAMBinding(ctx, c.pubsubClient.Topic(binding.ResourceID).IAM(), binding.Role, member)
	case "pubsub_subscription":
		return removeStandardIAMBinding(ctx, c.pubsubClient.Subscription(binding.ResourceID).IAM(), binding.Role, member)
	case "gcs_bucket":
		return removeStandardIAMBinding(ctx, c.storageClient.Bucket(binding.ResourceID).IAM(), binding.Role, member)
	case "secret":
		handle := &secretIAMHandle{
			client:     c.secretsClient,
			resourceID: fmt.Sprintf("projects/%s/secrets/%s", c.projectID, binding.ResourceID),
		}
		return removeStandardIAMBinding(ctx, handle, binding.Role, member)
	case "cloudrun_service":
		return c.removeCloudRunServiceIAMBinding(ctx, binding.ResourceLocation, binding.ResourceID, binding.Role, member)
	default:
		return fmt.Errorf("unsupported resource type for IAM binding removal: %s", binding.ResourceType)
	}
}

// EnsureServiceAccountExists checks if a service account exists and creates it if it doesn't.
// It is idempotent and returns the service account's email address.
func (c *GoogleIAMClient) EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error) {
	accountID := strings.Split(accountName, "@")[0]
	email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, c.projectID)
	resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", c.projectID, email)

	_, err := c.iamAdminClient.GetServiceAccount(ctx, &adminpb.GetServiceAccountRequest{Name: resourceName})
	if err == nil {
		log.Printf("IAM check: Service account %s already exists.", email)
		return email, nil
	}

	if status.Code(err) != codes.NotFound {
		return "", fmt.Errorf("failed to check for service account %s: %w", email, err)
	}

	createReq := &adminpb.CreateServiceAccountRequest{
		Name:           "projects/" + c.projectID,
		AccountId:      accountID,
		ServiceAccount: &adminpb.ServiceAccount{DisplayName: "Service Account for " + accountID},
	}
	sa, err := c.iamAdminClient.CreateServiceAccount(ctx, createReq)
	if err != nil {
		return "", fmt.Errorf("failed to create service account %s: %w", email, err)
	}
	return sa.Email, nil
}

// DeleteServiceAccount deletes a service account. It does not return an error if the account is already gone.
func (c *GoogleIAMClient) DeleteServiceAccount(ctx context.Context, accountName string) error {
	accountID := strings.Split(accountName, "@")[0]
	email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, c.projectID)
	resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", c.projectID, email)
	req := &adminpb.DeleteServiceAccountRequest{Name: resourceName}

	err := c.iamAdminClient.DeleteServiceAccount(ctx, req)
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to delete service account %s: %w", email, err)
	}

	if err == nil {
		log.Info().Str("email", email).Msg("Successfully deleted service account.")
	}
	return nil
}

// Close gracefully closes all underlying client connections.
func (c *GoogleIAMClient) Close() error {
	var errs []string
	if err := c.iamAdminClient.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("iamAdminClient: %v", err))
	}
	if err := c.pubsubClient.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("pubsubClient: %v", err))
	}
	if err := c.storageClient.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("storageClient: %v", err))
	}
	if err := c.bigqueryClient.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("bigqueryClient: %v", err))
	}
	if err := c.secretsClient.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("secretsClient: %v", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors while closing clients: %s", strings.Join(errs, "; "))
	}
	return nil
}

// --- Standard IAM Handling ---

// addStandardIAMBinding uses the "get-modify-set" pattern on a resource's iam.Handle.
// This pattern is common for many GCP services like Pub/Sub, Storage, etc.
func addStandardIAMBinding(ctx context.Context, handle iamHandle, role, member string) error {
	policy, err := handle.Policy(ctx)
	if err != nil {
		return fmt.Errorf("failed to get policy: %w", err)
	}

	policy.Add(member, iam.RoleName(role))

	err = handle.SetPolicy(ctx, policy)
	if err != nil {
		log.Error().Err(err).Str("role", role).Str("member", member).Msg("GoogleIAMClient: FAILED to set IAM policy.")
		return fmt.Errorf("failed to set policy: %w", err)
	}

	log.Info().Str("role", role).Str("member", member).Msg("GoogleIAMClient: Successfully set IAM policy.")
	return nil
}

// removeStandardIAMBinding uses the same "get-modify-set" pattern to remove a member.
func removeStandardIAMBinding(ctx context.Context, handle iamHandle, role, member string) error {
	policy, err := handle.Policy(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil // Resource doesn't exist, so binding is already gone.
		}
		return fmt.Errorf("failed to get policy for removal: %w", err)
	}
	policy.Remove(member, iam.RoleName(role))
	return handle.SetPolicy(ctx, policy)
}

// --- Specialized IAM Handlers ---

// addBigQueryDatasetIAMBinding modifies the Access Control List (ACL) of a BigQuery dataset.
// BigQuery's IAM model is older and does not use the standard iam.Handle. Instead, permissions
// are added as AccessEntry items to the dataset's metadata.
func (c *GoogleIAMClient) addBigQueryDatasetIAMBinding(ctx context.Context, datasetID, role, member string) error {
	dataset := c.bigqueryClient.Dataset(datasetID)
	meta, err := dataset.Metadata(ctx)
	if err != nil {
		return err
	}
	update := bigquery.DatasetMetadataToUpdate{
		Access: append(meta.Access, &bigquery.AccessEntry{
			Role:       bigquery.AccessRole(role),
			EntityType: bigquery.IAMMemberEntity,
			Entity:     member, // The Entity must be the full IAM member identifier (e.g., "serviceAccount:...")
		}),
	}
	_, err = dataset.Update(ctx, update, meta.ETag)
	return err
}

func (c *GoogleIAMClient) removeBigQueryDatasetIAMBinding(ctx context.Context, datasetID, role, member string) error {
	dataset := c.bigqueryClient.Dataset(datasetID)
	meta, err := dataset.Metadata(ctx)
	if err != nil {
		return err
	}
	var updatedAccess []*bigquery.AccessEntry
	for _, entry := range meta.Access {
		// Add all entries to the new list except the one we want to remove.
		if !(entry.Role == bigquery.AccessRole(role) && entry.EntityType == bigquery.IAMMemberEntity && entry.Entity == member) {
			updatedAccess = append(updatedAccess, entry)
		}
	}
	update := bigquery.DatasetMetadataToUpdate{Access: updatedAccess}
	_, err = dataset.Update(ctx, update, meta.ETag)
	return err
}

// addCloudRunServiceIAMBinding handles IAM for Cloud Run, which uses its own distinct IAM policy format.
func (c *GoogleIAMClient) addCloudRunServiceIAMBinding(ctx context.Context, location, serviceID, role, member string) error {
	if location == "" {
		return fmt.Errorf("location is required for Cloud Run service IAM binding but was not provided")
	}
	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", c.projectID, location, serviceID)

	policy, err := c.runService.Projects.Locations.Services.GetIamPolicy(fullServiceName).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get IAM policy for Cloud Run service %s: %w", serviceID, err)
	}

	var bindingToModify *run.GoogleIamV1Binding
	for _, b := range policy.Bindings {
		if b.Role == role {
			bindingToModify = b
			break
		}
	}

	if bindingToModify == nil {
		// If no binding for this role exists, create one.
		bindingToModify = &run.GoogleIamV1Binding{Role: role}
		policy.Bindings = append(policy.Bindings, bindingToModify)
	}

	// Check if the member is already present.
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
		log.Info().Str("member", member).Str("role", role).Str("service", serviceID).Msg("Member already has role on Cloud Run service. No changes needed.")
		return nil
	}

	setPolicyRequest := &run.GoogleIamV1SetIamPolicyRequest{Policy: policy}
	_, err = c.runService.Projects.Locations.Services.SetIamPolicy(fullServiceName, setPolicyRequest).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to set IAM policy for Cloud Run service %s: %w", serviceID, err)
	}

	log.Info().Str("member", member).Str("role", role).Str("service", serviceID).Msg("Successfully added IAM binding to Cloud Run service.")
	return nil
}

func (c *GoogleIAMClient) removeCloudRunServiceIAMBinding(ctx context.Context, location, serviceID, role, member string) error {
	if location == "" {
		return fmt.Errorf("location is required for Cloud Run service IAM binding but was not provided")
	}
	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", c.projectID, location, serviceID)

	policy, err := c.runService.Projects.Locations.Services.GetIamPolicy(fullServiceName).Context(ctx).Do()
	if err != nil {
		if status.Code(err) == codes.NotFound {
			log.Warn().Str("service", serviceID).Msg("Cloud Run service not found, cannot remove IAM binding.")
			return nil
		}
		return fmt.Errorf("failed to get IAM policy for Cloud Run service %s: %w", serviceID, err)
	}

	bindingModified := false
	for _, b := range policy.Bindings {
		if b.Role == role {
			var newMembers []string
			for _, m := range b.Members {
				if m != member {
					newMembers = append(newMembers, m)
				} else {
					bindingModified = true // We found and are removing the member.
				}
			}
			b.Members = newMembers
		}
	}

	if !bindingModified {
		log.Info().Str("member", member).Str("role", role).Str("service", serviceID).Msg("Member/role binding not found on Cloud Run service. No changes needed.")
		return nil
	}

	setPolicyRequest := &run.GoogleIamV1SetIamPolicyRequest{Policy: policy}
	_, err = c.runService.Projects.Locations.Services.SetIamPolicy(fullServiceName, setPolicyRequest).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to set updated IAM policy for Cloud Run service %s: %w", serviceID, err)
	}

	log.Info().Str("member", member).Str("role", role).Str("service", serviceID).Msg("Successfully removed IAM binding from Cloud Run service.")
	return nil
}

// --- Unrefactored but necessary functions ---

// The following functions are included for completeness but were not the focus of this refactoring pass.
// Their style and documentation may not yet be fully aligned.

func (c *GoogleIAMClient) ListServiceAccounts(ctx context.Context) ([]*adminpb.ServiceAccount, error) {
	req := &adminpb.ListServiceAccountsRequest{
		Name: fmt.Sprintf("projects/%s", c.projectID),
	}
	it := c.iamAdminClient.ListServiceAccounts(ctx, req)
	var accounts []*adminpb.ServiceAccount
	for {
		acc, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate service accounts: %w", err)
		}
		accounts = append(accounts, acc)
	}
	return accounts, nil
}

func (c *GoogleIAMClient) AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error {
	resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", c.projectID, serviceAccountEmail)
	policy, err := c.iamAdminClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: resourceName})
	if err != nil {
		return fmt.Errorf("failed to get IAM policy for SA %s: %w", serviceAccountEmail, err)
	}
	policy.Add(member, iam.RoleName(role))
	_, err = c.iamAdminClient.SetIamPolicy(ctx, &admin.SetIamPolicyRequest{
		Resource: resourceName,
		Policy:   policy,
	})
	if err != nil {
		return fmt.Errorf("failed to set IAM policy for SA %s: %w", serviceAccountEmail, err)
	}
	log.Info().Str("member", member).Str("role", role).Str("on_sa", serviceAccountEmail).Msg("Successfully added IAM binding to service account.")
	return nil
}

func (c *GoogleIAMClient) AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error {
	arClient, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create artifactregistry client: %w", err)
	}
	defer func() {
		_ = arClient.Close()
	}()
	repoResource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", c.projectID, location, repositoryID)
	policy, err := arClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: repoResource})
	if err != nil {
		return fmt.Errorf("failed to get IAM policy for repository %s: %w", repositoryID, err)
	}
	var bindingToModify *iampb.Binding
	for _, b := range policy.Bindings {
		if b.Role == role {
			bindingToModify = b
			break
		}
	}
	if bindingToModify == nil {
		bindingToModify = &iampb.Binding{
			Role:    role,
			Members: []string{},
		}
		policy.Bindings = append(policy.Bindings, bindingToModify)
	}
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
		log.Info().Msgf("Member %s already has role %s on repository %s. No changes needed.", member, role, repositoryID)
		return nil
	}
	_, err = arClient.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: repoResource,
		Policy:   policy,
	})
	if err != nil {
		return fmt.Errorf("failed to set IAM policy for repository %s: %w", repositoryID, err)
	}
	log.Info().Msgf("Successfully granted role %s to %s on repository %s.", role, member, repositoryID)
	return nil
}
