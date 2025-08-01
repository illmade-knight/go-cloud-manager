package orchestration

import (
	"context"
	"fmt"
	"os"

	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMOrchestrator is responsible for the high-level orchestration of all IAM policies for an architecture.
// It uses components from the `iam` package to plan roles, create service accounts, and apply bindings.
type IAMOrchestrator struct {
	arch                        *servicemanager.MicroserviceArchitecture
	logger                      zerolog.Logger
	iamClient                   iam.IAMClient
	iamManager                  iam.IAMManager
	iamProjectManager           *iam.IAMProjectManager
	planner                     *iam.RolePlanner
	createdServiceAccountEmails []string
}

// NewIAMOrchestrator creates a new orchestrator focused solely on IAM.
// It initializes the necessary clients and managers from the `iam` package.
func NewIAMOrchestrator(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*IAMOrchestrator, error) {
	var iamClient iam.IAMClient
	var err error

	// Conditionally create a real Google IAM client or a pooled test client
	// based on the presence of an environment variable. This is useful for integration testing.
	if os.Getenv("TEST_SA_POOL_MODE") == "true" {
		prefix := os.Getenv("TEST_SA_POOL_PREFIX")
		if prefix == "" {
			prefix = "test-pool-sa-"
		}
		iamClient, err = iam.NewTestIAMClient(ctx, arch.ProjectID, logger, prefix)
	} else {
		iamClient, err = iam.NewGoogleIAMClient(ctx, arch.ProjectID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create iamClient for orchestrator: %w", err)
	}

	iamProjectManager, err := iam.NewIAMProjectManager(ctx, arch.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create iam project manager: %w", err)
	}

	iamManager, err := iam.NewIAMManager(iamClient, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create iam manager: %w", err)
	}

	return &IAMOrchestrator{
		arch:                        arch,
		logger:                      logger.With().Str("component", "IAMOrchestrator").Logger(),
		iamClient:                   iamClient,
		iamManager:                  iamManager,
		iamProjectManager:           iamProjectManager,
		planner:                     iam.NewRolePlanner(logger),
		createdServiceAccountEmails: make([]string, 0),
	}, nil
}

// ApplyIAMForDataflow handles IAM setup for all services within a single dataflow.
// It iterates through each service, delegates to the iam.IAMManager to apply policies,
// and collects the resulting service account emails.
func (o *IAMOrchestrator) ApplyIAMForDataflow(ctx context.Context, dataflowName string) (map[string]string, error) {
	dataflow, ok := o.arch.Dataflows[dataflowName]
	if !ok {
		return nil, fmt.Errorf("dataflow '%s' not found in architecture", dataflowName)
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("Applying IAM for all services in dataflow.")
	serviceEmails := make(map[string]string)

	for serviceName, serviceSpec := range dataflow.Services {
		// Delegate the core logic of planning and applying roles to the IAMManager.
		err := o.iamManager.ApplyIAMForService(ctx, o.arch, dataflowName, serviceName)
		if err != nil {
			return nil, fmt.Errorf("failed to apply IAM for service '%s' in dataflow '%s': %w", serviceName, dataflowName, err)
		}

		// After applying, confirm the service account email and track it for later use (e.g., deployment).
		saEmail, err := o.iamClient.EnsureServiceAccountExists(ctx, serviceSpec.ServiceAccount)
		if err != nil {
			return nil, fmt.Errorf("failed to confirm service account email for '%s': %w", serviceName, err)
		}
		serviceEmails[serviceName] = saEmail
		o.createdServiceAccountEmails = append(o.createdServiceAccountEmails, saEmail)
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("Successfully applied IAM for all services in dataflow.")
	return serviceEmails, nil
}

// SetupServiceDirectorIAM ensures the ServiceDirector's service account exists and grants it
// the necessary project-level administrative roles to manage other resources.
func (o *IAMOrchestrator) SetupServiceDirectorIAM(ctx context.Context) (map[string]string, error) {
	o.logger.Info().Msg("Starting ServiceDirector IAM setup...")
	serviceEmails := make(map[string]string)

	// 1. Plan the required project-level roles.
	requiredRoles, err := o.planner.PlanRolesForServiceDirector(o.arch)
	if err != nil {
		return nil, fmt.Errorf("failed to plan IAM roles for ServiceDirector: %w", err)
	}

	// 2. Ensure the ServiceDirector's service account exists.
	serviceName := o.arch.ServiceManagerSpec.Name
	saName := o.arch.ServiceManagerSpec.ServiceAccount
	if saName == "" {
		saName = serviceName + "-sa"
	}
	saEmail, err := o.iamClient.EnsureServiceAccountExists(ctx, saName)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure ServiceDirector service account exists: %w", err)
	}
	o.logger.Info().Str("email", saEmail).Msg("ServiceDirector service account is ready.")
	o.createdServiceAccountEmails = append(o.createdServiceAccountEmails, saEmail)
	serviceEmails[serviceName] = saEmail

	// 3. Grant the planned project-level roles to the service account.
	member := "serviceAccount:" + saEmail
	for _, role := range requiredRoles {
		o.logger.Info().Str("role", role).Str("member", member).Msg("Granting project-level role to ServiceDirector service account.")
		err = o.iamProjectManager.AddProjectIAMBinding(ctx, member, role)
		if err != nil {
			return nil, fmt.Errorf("failed to grant project role '%s' to ServiceDirector SA: %w", role, err)
		}
	}

	// 4. Grant the Cloud Build service account permission to impersonate the ServiceDirector SA.
	// This is necessary for Cloud Build to deploy the ServiceDirector service with the correct identity.
	projectNumber, err := o.GetProjectNumber(ctx, o.arch.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project number: %w", err)
	}
	cloudBuildSaMember := fmt.Sprintf("serviceAccount:%s@cloudbuild.gserviceaccount.com", projectNumber)
	err = o.iamClient.AddMemberToServiceAccountRole(ctx, saEmail, cloudBuildSaMember, "roles/iam.serviceAccountUser")
	if err != nil {
		return nil, fmt.Errorf("failed to grant 'actAs' permission to Cloud Build SA: %w", err)
	}

	o.logger.Info().Msg("ServiceDirector IAM setup complete.")
	return serviceEmails, nil
}

// Teardown cleans up all service accounts created during the orchestration run.
// If using the TestIAMClient, this will be a "fake" delete that returns accounts to the pool.
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

	o.logger.Info().Msg("IAMOrchestrator cleanup finished.")
	return nil
}

// GetProjectNumber retrieves the numeric ID for a given project ID string.
func (o *IAMOrchestrator) GetProjectNumber(ctx context.Context, projectID string) (string, error) {
	c, err := resourcemanager.NewProjectsClient(ctx)
	if err != nil {
		return "", fmt.Errorf("resourcemanager.NewProjectsClient: %w", err)
	}
	defer func() {
		_ = c.Close()
	}()

	req := &resourcemanagerpb.GetProjectRequest{
		Name: fmt.Sprintf("projects/%s", projectID),
	}

	project, err := c.GetProject(ctx, req)
	if err != nil {
		return "", fmt.Errorf("GetProject: %w", err)
	}

	// The project name is returned as "projects/123456789". We just want the number.
	projectNumber := project.Name[len("projects/"):]
	return projectNumber, nil
}
