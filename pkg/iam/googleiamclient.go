package iam

import (
	"context"
	"fmt"
	"strings"
	"time"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/iam"
	"cloud.google.com/go/iam/admin/apiv1"
	"cloud.google.com/go/iam/admin/apiv1/adminpb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/pubsub"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/storage"
	"github.com/rs/zerolog/log"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// iamHandle is a private interface abstracting the common IAM methods.
type iamHandle interface {
	Policy(ctx context.Context) (*iam.Policy, error)
	SetPolicy(ctx context.Context, p *iam.Policy) error
}

// secretIAMHandle is an adapter to make the Secret Manager's IAM client
// conform to the iamHandle interface.
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

// GoogleIAMClient is the concrete implementation of the IAMClient interface for Google Cloud.
type GoogleIAMClient struct {
	projectID              string
	iamAdminClient         *admin.IamClient
	bigqueryAdminClient    *BigQueryIAMManager
	pubsubClient           *pubsub.Client
	storageClient          *storage.Client
	secretsClient          *secretmanager.Client
	runService             *run.Service
	artifactRegistryClient *artifactregistry.Client
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
	bigQueryIAMManager, err := NewBigQueryIAMManager(bqClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create bigquery manager: %w", err)
	}
	secretsClient, err := secretmanager.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create secretmanager client: %w", err)
	}
	runService, err := run.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create run service: %w", err)
	}
	arClient, err := artifactregistry.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create artifactregistry client: %w", err)
	}
	return &GoogleIAMClient{
		projectID:              projectID,
		iamAdminClient:         adminClient,
		pubsubClient:           psClient,
		storageClient:          gcsClient,
		bigqueryAdminClient:    bigQueryIAMManager,
		secretsClient:          secretsClient,
		runService:             runService,
		artifactRegistryClient: arClient,
	}, nil
}

// EnsureServiceAccountExists gets or creates a service account.
func (c *GoogleIAMClient) EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error) {
	var accountID string
	var email string
	if strings.Contains(accountName, "@") {
		email = accountName
		accountID = strings.Split(accountName, "@")[0]
	} else {
		accountID = accountName
		email = fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, c.projectID)
	}
	resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", c.projectID, email)
	_, err := c.iamAdminClient.GetServiceAccount(ctx, &adminpb.GetServiceAccountRequest{Name: resourceName})
	if err == nil {
		return email, nil
	}
	if status.Code(err) != codes.NotFound {
		return "", fmt.Errorf("failed to check for service account %s: %w", email, err)
	}
	createReq := &adminpb.CreateServiceAccountRequest{
		Name:           "projects/" + c.projectID,
		AccountId:      accountID,
		ServiceAccount: &adminpb.ServiceAccount{DisplayName: "SA for " + accountID},
	}
	sa, err := c.iamAdminClient.CreateServiceAccount(ctx, createReq)
	if err != nil {
		return "", fmt.Errorf("failed to create service account %s: %w", email, err)
	}
	return sa.Email, nil
}

// GetServiceAccount checks if a service account exists.
func (c *GoogleIAMClient) GetServiceAccount(ctx context.Context, accountEmail string) error {
	resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", c.projectID, accountEmail)
	_, err := c.iamAdminClient.GetServiceAccount(ctx, &adminpb.GetServiceAccountRequest{Name: resourceName})
	return err
}

// ApplyIAMPolicy sets the IAM policy for a resource.
// For most resources, this atomically REPLACES the entire policy with the provided one.
// REFACTOR: For Cloud Run services, this operation is ADDITIVE to avoid removing
// essential default permissions. It adds the specified members and roles without
// affecting existing ones.
func (c *GoogleIAMClient) ApplyIAMPolicy(ctx context.Context, binding PolicyBinding) error {
	var handle iamHandle
	switch binding.ResourceType {
	case "pubsub_topic":
		handle = c.pubsubClient.Topic(binding.ResourceID).IAM()
	case "pubsub_subscription":
		handle = c.pubsubClient.Subscription(binding.ResourceID).IAM()
	case "bigquery_table":
		ids := strings.Split(binding.ResourceID, ":")
		if len(ids) != 2 {
			return fmt.Errorf("invalid bigquery_table ResourceID: %s", binding.ResourceID)
		}
		handle = c.bigqueryAdminClient.client.Dataset(ids[0]).Table(ids[1]).IAM()
	case "gcs_bucket":
		handle = c.storageClient.Bucket(binding.ResourceID).IAM()
	case "secret":
		handle = &secretIAMHandle{
			client:     c.secretsClient,
			resourceID: fmt.Sprintf("projects/%s/secrets/%s", c.projectID, binding.ResourceID),
		}
	case "cloudrun_service":
		// This case is intentionally additive. We loop through the desired bindings
		// and add them one by one, preserving existing default IAM policies.
		for role, members := range binding.MemberRoles {
			for _, member := range members {
				err := c.addCloudRunServiceIAMBinding(ctx, binding.ResourceLocation, binding.ResourceID, role, member)
				if err != nil {
					return fmt.Errorf("failed to add Cloud Run binding for member '%s' with role '%s': %w", member, role, err)
				}
			}
		}
		return nil // Return early as we've completed the additive operation.
	default:
		return fmt.Errorf("ApplyIAMPolicy is not implemented for resource type: %s", binding.ResourceType)
	}
	return setFullIAMPolicy(ctx, handle, binding.MemberRoles)
}

func setFullIAMPolicy(ctx context.Context, handle iamHandle, memberRoles map[string][]string) error {
	const maxRetries = 5
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		policy, err := handle.Policy(ctx)
		if err != nil {
			if isRetriableError(err) {
				lastErr = err
				time.Sleep(time.Duration(i*100+50) * time.Millisecond)
				continue
			}
			return fmt.Errorf("failed to get policy: %w", err)
		}
		policy.InternalProto.Bindings = nil
		for role, members := range memberRoles {
			for _, member := range members {
				policy.Add(member, iam.RoleName(role))
			}
		}
		err = handle.SetPolicy(ctx, policy)
		if err == nil {
			return nil
		}
		lastErr = err
		if st, ok := status.FromError(err); ok && st.Code() == codes.Aborted {
			time.Sleep(time.Duration(i*100) * time.Millisecond)
			continue
		}
		if isRetriableError(err) {
			time.Sleep(time.Duration(i*100+50) * time.Millisecond)
			continue
		}
		return fmt.Errorf("failed to set policy: %w", err)
	}
	return fmt.Errorf("failed to set IAM policy after %d retries: %w", maxRetries, lastErr)
}

func (c *GoogleIAMClient) AddResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error {
	const maxRetries = 5
	const retryDelay = 3 * time.Second
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		var err error
		var handle iamHandle
		switch binding.ResourceType {
		case "bigquery_dataset":
			err = c.bigqueryAdminClient.AddDatasetIAMBinding(ctx, binding.ResourceID, binding.Role, member)
		case "bigquery_table":
			ids := strings.Split(binding.ResourceID, ":")
			if len(ids) != 2 {
				return fmt.Errorf("invalid bigquery_table ResourceID: %s", binding.ResourceID)
			}
			handle = c.bigqueryAdminClient.client.Dataset(ids[0]).Table(ids[1]).IAM()
			err = addStandardIAMBinding(ctx, handle, binding.Role, member)
		case "pubsub_topic":
			handle = c.pubsubClient.Topic(binding.ResourceID).IAM()
			err = addStandardIAMBinding(ctx, handle, binding.Role, member)
		case "pubsub_subscription":
			handle = c.pubsubClient.Subscription(binding.ResourceID).IAM()
			err = addStandardIAMBinding(ctx, handle, binding.Role, member)
		case "gcs_bucket":
			handle = c.storageClient.Bucket(binding.ResourceID).IAM()
			err = addStandardIAMBinding(ctx, handle, binding.Role, member)
		case "secret":
			handle = &secretIAMHandle{
				client:     c.secretsClient,
				resourceID: fmt.Sprintf("projects/%s/secrets/%s", c.projectID, binding.ResourceID),
			}
			err = addStandardIAMBinding(ctx, handle, binding.Role, member)
		case "cloudrun_service":
			err = c.addCloudRunServiceIAMBinding(ctx, binding.ResourceLocation, binding.ResourceID, binding.Role, member)
		default:
			err = fmt.Errorf("unsupported resource type for IAM binding: %s", binding.ResourceType)
		}
		if err == nil {
			return nil
		}
		lastErr = err
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			time.Sleep(retryDelay)
			continue
		}
		return err
	}
	return fmt.Errorf("failed to add IAM binding after %d retries: %w", maxRetries, lastErr)
}

func (c *GoogleIAMClient) CheckResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) (bool, error) {
	const maxRetries = 5
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		var policy *iam.Policy
		var err error
		switch binding.ResourceType {
		case "bigquery_dataset":
			return c.bigqueryAdminClient.CheckDatasetIAMBinding(ctx, binding.ResourceID, binding.Role, member)
		case "bigquery_table":
			ids := strings.Split(binding.ResourceID, ":")
			if len(ids) != 2 {
				return false, fmt.Errorf("invalid bigquery_table ResourceID: %s", binding.ResourceID)
			}
			policy, err = c.bigqueryAdminClient.client.Dataset(ids[0]).Table(ids[1]).IAM().Policy(ctx)
		case "pubsub_topic":
			policy, err = c.pubsubClient.Topic(binding.ResourceID).IAM().Policy(ctx)
		case "pubsub_subscription":
			policy, err = c.pubsubClient.Subscription(binding.ResourceID).IAM().Policy(ctx)
		case "cloudrun_service":
			// REFACTOR: Implemented check for Cloud Run service IAM.
			if binding.ResourceLocation == "" {
				return false, fmt.Errorf("location is required for Cloud Run service IAM check")
			}
			fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", c.projectID, binding.ResourceLocation, binding.ResourceID)
			runPolicy, getErr := c.runService.Projects.Locations.Services.GetIamPolicy(fullServiceName).Context(ctx).Do()
			if getErr != nil {
				err = getErr
			} else {
				for _, b := range runPolicy.Bindings {
					if b.Role == binding.Role {
						for _, m := range b.Members {
							if m == member {
								return true, nil
							}
						}
					}
				}
				return false, nil
			}
		default:
			return false, fmt.Errorf("unsupported resource type for IAM check: %s", binding.ResourceType)
		}

		if err == nil {
			return policy.HasRole(member, iam.RoleName(binding.Role)), nil
		}
		lastErr = err
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		if isRetriableError(err) {
			time.Sleep(time.Duration(i*100+50) * time.Millisecond)
			continue
		}
		return false, fmt.Errorf("could not get IAM policy for %s '%s': %w", binding.ResourceType, binding.ResourceID, err)
	}
	return false, fmt.Errorf("CheckResourceIAMBinding failed after %d retries: %w", maxRetries, lastErr)
}

func (c *GoogleIAMClient) RemoveResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error {
	var handle iamHandle
	switch binding.ResourceType {
	case "bigquery_dataset":
		return c.bigqueryAdminClient.RemoveDatasetIAMBinding(ctx, binding.ResourceID, binding.Role, member)
	case "bigquery_table":
		ids := strings.Split(binding.ResourceID, ":")
		if len(ids) != 2 {
			return fmt.Errorf("invalid bigquery_table ResourceID: %s", binding.ResourceID)
		}
		handle = c.bigqueryAdminClient.client.Dataset(ids[0]).Table(ids[1]).IAM()
	case "pubsub_topic":
		handle = c.pubsubClient.Topic(binding.ResourceID).IAM()
	case "pubsub_subscription":
		handle = c.pubsubClient.Subscription(binding.ResourceID).IAM()
	case "gcs_bucket":
		handle = c.storageClient.Bucket(binding.ResourceID).IAM()
	case "secret":
		handle = &secretIAMHandle{
			client:     c.secretsClient,
			resourceID: fmt.Sprintf("projects/%s/secrets/%s", c.projectID, binding.ResourceID),
		}
	case "cloudrun_service":
		// REFACTOR: Wired up the existing removal logic.
		return c.removeCloudRunServiceIAMBinding(ctx, binding.ResourceLocation, binding.ResourceID, binding.Role, member)
	default:
		return fmt.Errorf("unsupported resource type for IAM binding removal: %s", binding.ResourceType)
	}
	return removeStandardIAMBinding(ctx, handle, binding.Role, member)
}

func (c *GoogleIAMClient) DeleteServiceAccount(ctx context.Context, accountName string) error {
	accountID := strings.Split(accountName, "@")[0]
	email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, c.projectID)
	resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", c.projectID, email)
	req := &adminpb.DeleteServiceAccountRequest{Name: resourceName}
	err := c.iamAdminClient.DeleteServiceAccount(ctx, req)
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to delete service account %s: %w", email, err)
	}
	return nil
}

// AddMemberToServiceAccountRole adds a member to a role on a service account's IAM policy.
func (c *GoogleIAMClient) AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error {
	resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", c.projectID, serviceAccountEmail)

	const maxRetries = 5
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		policy, err := c.iamAdminClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: resourceName})
		if err != nil {
			return fmt.Errorf("failed to get IAM policy for SA %s: %w", serviceAccountEmail, err)
		}

		policy.Add(member, iam.RoleName(role))
		_, err = c.iamAdminClient.SetIamPolicy(ctx, &admin.SetIamPolicyRequest{
			Resource: resourceName,
			Policy:   policy,
		})

		if err == nil {
			log.Info().Str("member", member).Str("role", role).Str("on_sa", serviceAccountEmail).Msg("Successfully added IAM binding to service account.")
			return nil
		}
		lastErr = err

		if st, ok := status.FromError(err); ok && st.Code() == codes.Aborted {
			log.Warn().
				Err(err).
				Int("attempt", i+1).
				Str("sa", serviceAccountEmail).
				Msg("Concurrent modification detected while setting SA IAM policy. Retrying...")
			time.Sleep(time.Duration(i*100+50) * time.Millisecond)
			continue
		}

		return fmt.Errorf("failed to set IAM policy for SA %s: %w", serviceAccountEmail, err)
	}
	return fmt.Errorf("failed to set IAM policy for SA %s after %d retries: %w", serviceAccountEmail, maxRetries, lastErr)
}

func (c *GoogleIAMClient) AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error {
	repoResource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", c.projectID, location, repositoryID)
	policy, err := c.artifactRegistryClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: repoResource})
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
		bindingToModify = &iampb.Binding{Role: role, Members: []string{}}
		policy.Bindings = append(policy.Bindings, bindingToModify)
	}
	for _, m := range bindingToModify.Members {
		if m == member {
			return nil
		}
	}
	bindingToModify.Members = append(bindingToModify.Members, member)
	_, err = c.artifactRegistryClient.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: repoResource,
		Policy:   policy,
	})
	if err != nil {
		return fmt.Errorf("failed to set IAM policy for repository %s: %w", repositoryID, err)
	}
	return nil
}

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
	if err := c.bigqueryAdminClient.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("bigqueryClient: %v", err))
	}
	if err := c.secretsClient.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("secretsClient: %v", err))
	}
	if err := c.artifactRegistryClient.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("artifactRegistryClient: %v", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors while closing clients: %s", strings.Join(errs, "; "))
	}
	return nil
}

func isRetriableError(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unavailable, codes.ResourceExhausted, codes.Unauthenticated:
		return true
	default:
		return false
	}
}

func addStandardIAMBinding(ctx context.Context, handle iamHandle, role, member string) error {
	const maxRetries = 5
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		policy, err := handle.Policy(ctx)
		if err != nil {
			if isRetriableError(err) {
				lastErr = err
				time.Sleep(time.Duration(i*100+50) * time.Millisecond)
				continue
			}
			return fmt.Errorf("failed to get policy: %w", err)
		}
		policy.Add(member, iam.RoleName(role))
		err = handle.SetPolicy(ctx, policy)
		if err == nil {
			return nil
		}
		lastErr = err
		if st, ok := status.FromError(err); ok && st.Code() == codes.Aborted {
			time.Sleep(time.Duration(i*100) * time.Millisecond)
			continue
		}
		if isRetriableError(err) {
			time.Sleep(time.Duration(i*100+50) * time.Millisecond)
			continue
		}
		return fmt.Errorf("failed to set policy: %w", err)
	}
	return fmt.Errorf("failed to add IAM binding after %d retries: %w", maxRetries, lastErr)
}

func removeStandardIAMBinding(ctx context.Context, handle iamHandle, role, member string) error {
	const maxRetries = 5
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		policy, err := handle.Policy(ctx)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil
			}
			if isRetriableError(err) {
				lastErr = err
				time.Sleep(time.Duration(i*100+50) * time.Millisecond)
				continue
			}
			return fmt.Errorf("failed to get policy for removal: %w", err)
		}
		policy.Remove(member, iam.RoleName(role))
		err = handle.SetPolicy(ctx, policy)
		if err == nil {
			return nil
		}
		lastErr = err
		if st, ok := status.FromError(err); ok && st.Code() == codes.Aborted {
			time.Sleep(time.Duration(i*100) * time.Millisecond)
			continue
		}
		if isRetriableError(err) {
			time.Sleep(time.Duration(i*100+50) * time.Millisecond)
			continue
		}
		return fmt.Errorf("failed to set policy for removal: %w", err)
	}
	return fmt.Errorf("failed to remove IAM binding after %d retries: %w", maxRetries, lastErr)
}

func (c *GoogleIAMClient) addCloudRunServiceIAMBinding(ctx context.Context, location, serviceID, role, member string) error {
	if location == "" {
		return fmt.Errorf("location is required for Cloud Run service IAM binding")
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
		bindingToModify = &run.GoogleIamV1Binding{Role: role}
		policy.Bindings = append(policy.Bindings, bindingToModify)
	}
	for _, m := range bindingToModify.Members {
		if m == member {
			return nil
		}
	}
	bindingToModify.Members = append(bindingToModify.Members, member)
	setPolicyRequest := &run.GoogleIamV1SetIamPolicyRequest{Policy: policy}
	_, err = c.runService.Projects.Locations.Services.SetIamPolicy(fullServiceName, setPolicyRequest).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to set IAM policy for Cloud Run service %s: %w", serviceID, err)
	}
	return nil
}

func (c *GoogleIAMClient) removeCloudRunServiceIAMBinding(ctx context.Context, location, serviceID, role, member string) error {
	if location == "" {
		return fmt.Errorf("location is required for Cloud Run service IAM binding")
	}
	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", c.projectID, location, serviceID)
	policy, err := c.runService.Projects.Locations.Services.GetIamPolicy(fullServiceName).Context(ctx).Do()
	if err != nil {
		if status.Code(err) == codes.NotFound {
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
					bindingModified = true
				}
			}
			b.Members = newMembers
		}
	}
	if !bindingModified {
		return nil
	}
	setPolicyRequest := &run.GoogleIamV1SetIamPolicyRequest{Policy: policy}
	_, err = c.runService.Projects.Locations.Services.SetIamPolicy(fullServiceName, setPolicyRequest).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to set updated IAM policy for Cloud Run service %s: %w", serviceID, err)
	}
	return nil
}
