package orchestration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/iam/apiv1/iampb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// IAMOrchestrator is responsible for the high-level orchestration of all IAM policies for an architecture.
type IAMOrchestrator struct {
	arch                        *servicemanager.MicroserviceArchitecture
	logger                      zerolog.Logger
	iamClient                   iam.IAMClient
	iamProjectManager           *iam.GoogleIAMProjectClient
	planner                     *iam.RolePlanner
	createdServiceAccountEmails []string
}

// NewIAMOrchestrator creates a new orchestrator focused solely on IAM.
func NewIAMOrchestrator(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, clientOpts ...option.ClientOption) (*IAMOrchestrator, error) {
	var iamClient iam.IAMClient
	var err error

	if os.Getenv("TEST_SA_POOL_MODE") == "true" {
		prefix := os.Getenv("TEST_SA_POOL_PREFIX")
		if prefix == "" {
			prefix = "test-pool-sa-"
		}
		iamClient, err = iam.NewTestIAMClient(ctx, arch.ProjectID, logger, prefix)
	} else {
		iamClient, err = iam.NewGoogleIAMClient(ctx, arch.ProjectID, clientOpts...)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create iamClient for orchestrator: %w", err)
	}

	iamProjectManager, err := iam.NewGoogleIAMProjectClient(ctx, arch.ProjectID, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create iam project manager: %w", err)
	}

	return &IAMOrchestrator{
		arch:                        arch,
		logger:                      logger.With().Str("component", "IAMOrchestrator").Logger(),
		iamClient:                   iamClient,
		iamProjectManager:           iamProjectManager,
		planner:                     iam.NewRolePlanner(logger),
		createdServiceAccountEmails: make([]string, 0),
	}, nil
}

// EnsureDataflowSAsExist creates the necessary service accounts for a dataflow.
func (o *IAMOrchestrator) EnsureDataflowSAsExist(ctx context.Context, dataflowName string) (map[string]string, error) {
	dataflow, ok := o.arch.Dataflows[dataflowName]
	if !ok {
		return nil, fmt.Errorf("dataflow '%s' not found in architecture", dataflowName)
	}
	o.logger.Info().Str("dataflow", dataflowName).Msg("Ensuring service accounts exist for all services in dataflow.")

	serviceEmails := make(map[string]string)
	for serviceName, serviceSpec := range dataflow.Services {
		saEmail, err := o.iamClient.EnsureServiceAccountExists(ctx, serviceSpec.ServiceAccount)
		if err != nil {
			return nil, fmt.Errorf("failed to ensure service account '%s' for service '%s': %w", serviceSpec.ServiceAccount, serviceName, err)
		}
		serviceEmails[serviceName] = saEmail
		o.createdServiceAccountEmails = append(o.createdServiceAccountEmails, saEmail)
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("Successfully ensured all service accounts exist.")
	return serviceEmails, nil
}

// PollForSAExistence waits for a newly created service account to propagate throughout Google Cloud.
func (o *IAMOrchestrator) PollForSAExistence(ctx context.Context, accountEmail string, timeout time.Duration) error {
	const pollInterval = 5 * time.Second

	o.logger.Debug().Str("email", accountEmail).Msg("Starting poll for SA existence...")
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timed out waiting for service account '%s' to propagate", accountEmail)
		case <-ticker.C:
			err := o.iamClient.GetServiceAccount(timeoutCtx, accountEmail)
			if err == nil {
				o.logger.Debug().Str("email", accountEmail).Msg("SA found.")
				return nil
			}
			if status.Code(err) != codes.NotFound {
				return fmt.Errorf("unexpected error while polling for SA '%s': %w", accountEmail, err)
			}
			o.logger.Debug().Str("email", accountEmail).Msg("SA not yet propagated, still waiting...")
		}
	}
}

// ApplyProjectLevelIAMForDataflow plans and applies all project-level IAM roles for a dataflow's services.
// Note: This function only applies the roles; it does not wait for them to propagate.
func (o *IAMOrchestrator) ApplyProjectLevelIAMForDataflow(ctx context.Context, dataflowName string, saEmails map[string]string) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Planning and applying project-level IAM roles.")

	iamPlan, err := o.planner.PlanRolesForApplicationServices(o.arch)
	if err != nil {
		return fmt.Errorf("failed to generate IAM plan for dataflow '%s': %w", dataflowName, err)
	}

	// Filter for only project-level bindings.
	var projectBindings []iam.IAMBinding
	for _, binding := range iamPlan {
		if binding.ResourceType == "project" {
			projectBindings = append(projectBindings, binding)
		}
	}

	if len(projectBindings) == 0 {
		o.logger.Info().Str("dataflow", dataflowName).Msg("No project-level IAM bindings to apply.")
		return nil
	}

	// Get the logical-name-to-email mapping for all SAs in the dataflow.
	dataflow, _ := o.arch.Dataflows[dataflowName]
	logicalNameToEmail := make(map[string]string)
	for serviceName, serviceSpec := range dataflow.Services {
		email, ok := saEmails[serviceName]
		if !ok {
			return fmt.Errorf("consistency error: could not find email for service '%s' needed for IAM binding", serviceName)
		}
		logicalNameToEmail[serviceSpec.ServiceAccount] = email
	}

	// Apply each project-level binding.
	for _, binding := range projectBindings {
		saEmail, ok := logicalNameToEmail[binding.ServiceAccount]
		if !ok {
			// This can happen if a binding is for a service not in this specific dataflow, which is valid.
			continue
		}
		member := "serviceAccount:" + saEmail
		o.logger.Info().Str("member", member).Str("role", binding.Role).Msg("Applying project-level IAM role")
		err := o.iamProjectManager.GrantFirestoreProjectRole(ctx, member, binding.Role)
		if err != nil {
			return err
		}
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("Successfully applied project-level IAM roles.")
	return nil
}

// REFACTOR: This new function separates the verification of project-level IAM from resource-level IAM.
// It will be called by the Conductor during the initial IAM setup phase to fail fast.
//
// VerifyProjectLevelIAMForDataflow polls project-level IAM policies until they reflect the planned state.
func (o *IAMOrchestrator) VerifyProjectLevelIAMForDataflow(ctx context.Context, dataflowName string, saEmails map[string]string, verificationTimeout time.Duration) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Verifying PROJECT-LEVEL IAM policy propagation...")
	iamPlan, err := o.planner.PlanRolesForApplicationServices(o.arch)
	if err != nil {
		return fmt.Errorf("failed to generate IAM plan for verification: %w", err)
	}

	var projectBindings []iam.IAMBinding
	for _, binding := range iamPlan {
		if binding.ResourceType == "project" {
			projectBindings = append(projectBindings, binding)
		}
	}

	if len(projectBindings) == 0 {
		o.logger.Info().Str("dataflow", dataflowName).Msg("No project-level IAM bindings to verify.")
		return nil
	}

	// Concurrently poll for all project-level bindings across all services in the dataflow.
	var wg sync.WaitGroup
	errs := make(chan error, len(projectBindings))

	// Get the logical-name-to-email mapping for all SAs in the dataflow.
	dataflow, _ := o.arch.Dataflows[dataflowName]
	logicalNameToEmail := make(map[string]string)
	for serviceName, serviceSpec := range dataflow.Services {
		email, ok := saEmails[serviceName]
		if !ok {
			return fmt.Errorf("consistency error: could not find email for service '%s' needed for IAM verification", serviceName)
		}
		logicalNameToEmail[serviceSpec.ServiceAccount] = email
	}

	for _, binding := range projectBindings {
		saEmail, ok := logicalNameToEmail[binding.ServiceAccount]
		if !ok {
			// This binding is not for a service in this dataflow, so we skip it.
			continue
		}
		member := "serviceAccount:" + saEmail

		wg.Add(1)
		go func(b iam.IAMBinding) {
			defer wg.Done()
			if err := o.pollForBinding(ctx, b, member, verificationTimeout); err != nil {
				errs <- err
			}
		}(binding)
	}
	wg.Wait()
	close(errs)

	var verificationErrors []string
	for err := range errs {
		verificationErrors = append(verificationErrors, err.Error())
	}
	if len(verificationErrors) > 0 {
		return fmt.Errorf("project-level IAM verification failed for dataflow '%s': %s", dataflowName, strings.Join(verificationErrors, "; "))
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("✅ Successfully verified all PROJECT-LEVEL IAM policies.")
	return nil
}

// REFACTOR: This new function handles verification for resource-level IAM, which can only happen
// after the ServiceDirector has created the underlying resources (e.g., topics, subscriptions).
//
// VerifyResourceLevelIAMForDataflow polls resource-level IAM policies until they reflect the planned state.
func (o *IAMOrchestrator) VerifyResourceLevelIAMForDataflow(ctx context.Context, dataflowName string, saEmails map[string]string, verificationTimeout time.Duration) error {
	o.logger.Info().Str("dataflow", dataflowName).Msg("Verifying RESOURCE-LEVEL IAM policy propagation...")
	iamPlan, err := o.planner.PlanRolesForApplicationServices(o.arch)
	if err != nil {
		return fmt.Errorf("failed to generate IAM plan for verification: %w", err)
	}

	dataflow, _ := o.arch.Dataflows[dataflowName]
	for serviceName, serviceSpec := range dataflow.Services {
		// Filter for resource-level bindings relevant to this specific service.
		var serviceResourceBindings []iam.IAMBinding
		for _, binding := range iamPlan {
			if binding.ServiceAccount == serviceSpec.ServiceAccount && binding.ResourceType != "project" {
				serviceResourceBindings = append(serviceResourceBindings, binding)
			}
		}
		if len(serviceResourceBindings) == 0 {
			continue
		}

		saEmail, ok := saEmails[serviceName]
		if !ok {
			return fmt.Errorf("could not find service account email for '%s' during verification", serviceName)
		}
		member := "serviceAccount:" + saEmail

		o.logger.Info().Str("service", serviceName).Int("bindings", len(serviceResourceBindings)).Msg("Concurrently polling for all RESOURCE-LEVEL IAM bindings...")
		var wg sync.WaitGroup
		errs := make(chan error, len(serviceResourceBindings))
		for _, binding := range serviceResourceBindings {
			wg.Add(1)
			go func(b iam.IAMBinding) {
				defer wg.Done()
				if err := o.pollForBinding(ctx, b, member, verificationTimeout); err != nil {
					errs <- err
				}
			}(binding)
		}
		wg.Wait()
		close(errs)

		var verificationErrors []string
		for err := range errs {
			verificationErrors = append(verificationErrors, err.Error())
		}
		if len(verificationErrors) > 0 {
			return fmt.Errorf("resource-level IAM verification failed for service '%s': %s", serviceName, strings.Join(verificationErrors, "; "))
		}
		o.logger.Info().Str("service", serviceName).Msg("All resource-level bindings verified successfully.")
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("✅ Successfully verified all RESOURCE-LEVEL IAM policies.")
	return nil
}

// pollForBinding polls a single IAM binding until it is found or a timeout occurs.
func (o *IAMOrchestrator) pollForBinding(ctx context.Context, binding iam.IAMBinding, member string, timeout time.Duration) error {
	const pollInterval = 5 * time.Second
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	o.logger.Debug().Str("member", member).Str("role", binding.Role).Str("resource", binding.ResourceID).Msg("Starting poll for binding...")
	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timed out waiting for binding to propagate: member '%s' on resource '%s' with role '%s'", member, binding.ResourceID, binding.Role)
		case <-ticker.C:
			found, err := o.iamClient.CheckResourceIAMBinding(timeoutCtx, binding, member)
			if err != nil {
				o.logger.Warn().Err(err).Msg("Polling check failed with an error, will retry.")
				continue
			}
			if found {
				o.logger.Debug().Str("member", member).Str("role", binding.Role).Str("resource", binding.ResourceID).Msg("Binding found.")
				return nil
			}
			o.logger.Debug().Str("member", member).Str("role", binding.Role).Str("resource", binding.ResourceID).Msg("Binding not yet propagated, still waiting...")
		}
	}
}

// SetupServiceDirectorIAM ensures the ServiceDirector's SA exists and grants it necessary project-level roles.
func (o *IAMOrchestrator) SetupServiceDirectorIAM(ctx context.Context) (map[string]string, error) {
	o.logger.Info().Msg("Starting ServiceDirector IAM setup...")
	serviceEmails := make(map[string]string)
	requiredRoles, err := o.planner.PlanRolesForServiceDirector(o.arch)
	if err != nil {
		return nil, fmt.Errorf("failed to plan IAM roles for ServiceDirector: %w", err)
	}
	serviceName := o.arch.ServiceManagerSpec.Name
	saName := o.arch.ServiceManagerSpec.ServiceAccount
	if saName == "" {
		saName = serviceName + "-sa"
	}

	saEmail, err := o.iamClient.EnsureServiceAccountExists(ctx, saName)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure ServiceDirector SA exists: %w", err)
	}
	o.logger.Info().Str("email", saEmail).Msg("ServiceDirector service account is ready.")
	o.createdServiceAccountEmails = append(o.createdServiceAccountEmails, saEmail)
	serviceEmails[serviceName] = saEmail

	member := "serviceAccount:" + saEmail
	for _, role := range requiredRoles {
		o.logger.Info().Str("role", role).Str("member", member).Msg("Granting project-level role to ServiceDirector SA.")
		err = o.iamProjectManager.AddProjectIAMBinding(ctx, member, role)
		if err != nil {
			return nil, fmt.Errorf("failed to grant project role '%s' to ServiceDirector SA: %w", role, err)
		}
	}
	projectNumber, err := o.GetProjectNumber(ctx, o.arch.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project number: %w", err)
	}
	cloudBuildSaMember := fmt.Sprintf("serviceAccount:%s@cloudbuild.gserviceaccount.com", projectNumber)
	err = o.iamClient.AddMemberToServiceAccountRole(ctx, saEmail, cloudBuildSaMember, "roles/iam.serviceAccountUser")
	if err != nil {
		return nil, fmt.Errorf("failed to grant 'actAs' permission to Cloud Build SA: %w", err)
	}

	o.logger.Info().Msg("ServiceDirector IAM grant phase complete.")
	return serviceEmails, nil
}

// REFACTOR: This new function is called by the Conductor to fix a race condition. It ensures
// that the roles granted to the Service Director have fully propagated before the Conductor
// attempts to deploy it.
//
// VerifyServiceDirectorIAM polls the project-level IAM policies for the ServiceDirector until they are effective.
func (o *IAMOrchestrator) VerifyServiceDirectorIAM(ctx context.Context, saEmails map[string]string, verificationTimeout time.Duration) error {
	o.logger.Info().Msg("Verifying ServiceDirector project-level IAM policy propagation...")
	requiredRoles, err := o.planner.PlanRolesForServiceDirector(o.arch)
	if err != nil {
		return fmt.Errorf("failed to plan IAM roles for ServiceDirector verification: %w", err)
	}

	serviceName := o.arch.ServiceManagerSpec.Name
	saEmail, ok := saEmails[serviceName]
	if !ok {
		return fmt.Errorf("could not find SA email for ServiceDirector '%s' during verification", serviceName)
	}
	member := "serviceAccount:" + saEmail

	var wg sync.WaitGroup
	errs := make(chan error, len(requiredRoles))
	o.logger.Info().Int("bindings", len(requiredRoles)).Msg("Concurrently polling for all ServiceDirector IAM bindings...")

	for _, role := range requiredRoles {
		binding := iam.IAMBinding{
			ResourceType: "project",
			ResourceID:   o.arch.ProjectID,
			Role:         role,
			// ServiceAccount field isn't needed by pollForBinding, which just uses the `member` string.
		}
		wg.Add(1)
		go func(b iam.IAMBinding) {
			defer wg.Done()
			if err := o.pollForBinding(ctx, b, member, verificationTimeout); err != nil {
				errs <- err
			}
		}(binding)
	}
	wg.Wait()
	close(errs)

	var verificationErrors []string
	for err := range errs {
		verificationErrors = append(verificationErrors, err.Error())
	}
	if len(verificationErrors) > 0 {
		return fmt.Errorf("ServiceDirector IAM verification failed: %s", strings.Join(verificationErrors, "; "))
	}

	o.logger.Info().Msg("✅ Successfully verified all ServiceDirector project-level IAM policies.")
	return nil
}

// Teardown cleans up service accounts created during the orchestration run.
func (o *IAMOrchestrator) Teardown(ctx context.Context) error {
	o.logger.Info().Int("created_count", len(o.createdServiceAccountEmails)).Msg("Starting IAMOrchestrator cleanup...")
	for _, email := range o.createdServiceAccountEmails {
		err := o.iamClient.DeleteServiceAccount(ctx, email)
		if err != nil {
			o.logger.Error().Err(err).Str("email", email).Msg("Failed during service account cleanup")
		}
	}
	if o.iamClient != nil {
		err := o.iamClient.Close()
		if err != nil {
			o.logger.Error().Err(err).Msg("Failed to close IAM client")
		}
	}
	return nil
}

// GetProjectNumber retrieves the numeric ID for a given project ID string.
func (o *IAMOrchestrator) GetProjectNumber(ctx context.Context, projectID string) (string, error) {
	c, err := resourcemanager.NewProjectsClient(ctx)
	if err != nil {
		return "", fmt.Errorf("resourcemanager.NewProjectsClient: %w", err)
	}
	defer func() { _ = c.Close() }()

	req := &resourcemanagerpb.GetProjectRequest{Name: fmt.Sprintf("projects/%s", projectID)}
	project, err := c.GetProject(ctx, req)
	if err != nil {
		return "", fmt.Errorf("GetProject: %w", err)
	}

	return strings.TrimPrefix(project.Name, "projects/"), nil
}

// PreflightChecks verifies that the identity running the Conductor has the
// minimum necessary permissions to perform its verification steps.
func (o *IAMOrchestrator) PreflightChecks(ctx context.Context) error {
	o.logger.Info().Msg("Running orchestrator preflight permission checks...")

	requiredPermissions := &iampb.TestIamPermissionsRequest{
		Resource: fmt.Sprintf("projects/%s", o.arch.ProjectID),
		Permissions: []string{
			"resourcemanager.projects.getIamPolicy",
			"pubsub.topics.getIamPolicy",
			"pubsub.subscriptions.getIamPolicy",
			"storage.buckets.getIamPolicy",
			"bigquery.tables.getIamPolicy",
			"run.services.getIamPolicy",
			"secretmanager.secrets.getIamPolicy",
			"artifactregistry.repositories.getIamPolicy",
		},
	}

	resp, err := o.iamProjectManager.TestProjectIAMBinding(ctx, requiredPermissions)
	if err != nil {
		return fmt.Errorf("failed to test project IAM permissions: %w", err)
	}

	var missingPermissions []string
	allowedMap := make(map[string]bool)
	for _, p := range resp.Permissions {
		allowedMap[p] = true
	}

	for _, p := range requiredPermissions.Permissions {
		if !allowedMap[p] {
			missingPermissions = append(missingPermissions, p)
		}
	}

	if len(missingPermissions) > 0 {
		return fmt.Errorf("orchestrator is missing required permissions: %v", missingPermissions)
	}

	o.logger.Info().Msg("✅ Orchestrator permissions are sufficient.")
	return nil
}
