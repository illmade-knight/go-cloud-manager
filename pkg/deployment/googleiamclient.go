package deployment

import (
	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/iam"
	"cloud.google.com/go/iam/admin/apiv1"
	"cloud.google.com/go/iam/admin/apiv1/adminpb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

type RepoConfig struct {
	ProjectID     string
	ProjectNumber string
	Name          string
	Location      string
}

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

// iamHandle is an unexported interface that abstracts the common methods
// from various resource-specific IAM handles, allowing for unified logic.
type iamHandle interface {
	Policy(ctx context.Context) (*iam.Policy, error)
	SetPolicy(ctx context.Context, p *iam.Policy) error
}

// GoogleIAMClient holds the necessary Google Cloud clients for IAM operations.
type GoogleIAMClient struct {
	projectID      string
	iamAdminClient *admin.IamClient
	pubsubClient   *pubsub.Client
	storageClient  *storage.Client
	bigqueryClient *bigquery.Client
}

// NewGoogleIAMClient creates a new, fully initialized adminClient for real Google Cloud IAM operations.
func NewGoogleIAMClient(ctx context.Context, projectID string, opts ...option.ClientOption) (IAMClient, error) {
	adminClient, err := admin.NewIamClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM admin adminClient: %w", err)
	}
	psClient, err := pubsub.NewClient(ctx, projectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub adminClient: %w", err)
	}
	gcsClient, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage adminClient: %w", err)
	}
	bqClient, err := bigquery.NewClient(ctx, projectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create bigquery adminClient: %w", err)
	}

	return &GoogleIAMClient{
		projectID:      projectID,
		iamAdminClient: adminClient,
		pubsubClient:   psClient,
		storageClient:  gcsClient,
		bigqueryClient: bqClient,
	}, nil
}

// EnsureServiceAccountExists creates a service account if it does not already exist.
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

func (c *GoogleIAMClient) AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error {
	// This client is specific to Artifact Registry
	arClient, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create artifactregistry client: %w", err)
	}
	defer arClient.Close()

	repoResource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", c.projectID, location, repositoryID)

	// 1. Get the current policy
	policy, err := arClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: repoResource})
	if err != nil {
		return fmt.Errorf("failed to get IAM policy for repository %s: %w", repositoryID, err)
	}

	// 2. Modify the policy to add the new member to the role
	var bindingToModify *iampb.Binding
	for _, b := range policy.Bindings {
		if b.Role == role {
			bindingToModify = b
			break
		}
	}

	if bindingToModify == nil {
		// If the role doesn't exist in the policy, create a new binding for it.
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
		return nil // No changes needed
	}

	// 3. Set the new, modified policy
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

// AddResourceIAMBinding uses the correct handle for the given resource type to add an IAM binding.
func (c *GoogleIAMClient) AddResourceIAMBinding(ctx context.Context, resourceType, resourceID, role, member string) error {
	switch resourceType {
	case "bigquery_dataset":
		return c.addBigQueryDatasetIAMBinding(ctx, resourceID, role, member)
	case "pubsub_topic":
		return addStandardIAMBinding(ctx, c.pubsubClient.Topic(resourceID).IAM(), role, member)
	case "pubsub_subscription":
		return addStandardIAMBinding(ctx, c.pubsubClient.Subscription(resourceID).IAM(), role, member)
	case "gcs_bucket":
		return addStandardIAMBinding(ctx, c.storageClient.Bucket(resourceID).IAM(), role, member)
	default:
		return fmt.Errorf("unsupported resource type for IAM binding: %s", resourceType)
	}
}

// RemoveResourceIAMBinding uses the correct handle for the given resource type to remove an IAM binding.
func (c *GoogleIAMClient) RemoveResourceIAMBinding(ctx context.Context, resourceType, resourceID, role, member string) error {
	switch resourceType {
	case "bigquery_dataset":
		return c.removeBigQueryDatasetIAMBinding(ctx, resourceID, role, member)
	case "pubsub_topic":
		return removeStandardIAMBinding(ctx, c.pubsubClient.Topic(resourceID).IAM(), role, member)
	case "pubsub_subscription":
		return removeStandardIAMBinding(ctx, c.pubsubClient.Subscription(resourceID).IAM(), role, member)
	case "gcs_bucket":
		return removeStandardIAMBinding(ctx, c.storageClient.Bucket(resourceID).IAM(), role, member)
	default:
		return fmt.Errorf("unsupported resource type for IAM binding removal: %s", resourceType)
	}
}

// --- Private Helper functions for Standard IAM ---

// addStandardIAMBinding is a helper for resources that use the standard iam.Policy object.
func addStandardIAMBinding(ctx context.Context, handle iamHandle, role, member string) error {
	policy, err := handle.Policy(ctx)
	if err != nil {
		return fmt.Errorf("failed to get policy: %w", err)
	}
	policy.Add(member, iam.RoleName(role))
	return handle.SetPolicy(ctx, policy)
}

// removeStandardIAMBinding is a helper for resources that use the standard iam.Policy object.
func removeStandardIAMBinding(ctx context.Context, handle iamHandle, role, member string) error {
	policy, err := handle.Policy(ctx)
	if err != nil {
		// If the policy doesn't exist, there's nothing to remove.
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("failed to get policy for removal: %w", err)
	}
	policy.Remove(member, iam.RoleName(role))
	return handle.SetPolicy(ctx, policy)
}

// --- BigQuery IAM (Handled separately) ---
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
			Entity:     strings.TrimPrefix(member, "serviceAccount:"),
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
	email := strings.TrimPrefix(member, "serviceAccount:")
	for _, entry := range meta.Access {
		if !(entry.Role == bigquery.AccessRole(role) && entry.EntityType == bigquery.IAMMemberEntity && entry.Entity == email) {
			updatedAccess = append(updatedAccess, entry)
		}
	}
	update := bigquery.DatasetMetadataToUpdate{Access: updatedAccess}
	_, err = dataset.Update(ctx, update, meta.ETag)
	return err
}

// --- Service Account IAM (Handled separately) ---

// setServiceAccountPolicyLowLevel is a helper that sets the IAM policy using the low-level protobuf types.
func (c *GoogleIAMClient) setServiceAccountPolicyLowLevel(ctx context.Context, resourceName string, policy *iampb.Policy) error {
	// The *admin.IamClient's SetIamPolicy method also takes a request object from the iampb package.
	_, err := c.iamAdminClient.SetIamPolicy(ctx, &admin.SetIamPolicyRequest{
		Resource: resourceName,
		Policy:   &iam.Policy{InternalProto: policy},
	})
	if err != nil {
		return fmt.Errorf("failed to set low-level IAM policy for %s: %w", resourceName, err)
	}
	return nil
}
