package orchestration

import (
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"context"
	"fmt"
	"os"

	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMOrchestrator is responsible for planning and applying all IAM policies for an architecture.
type IAMOrchestrator struct {
	arch                        *servicemanager.MicroserviceArchitecture
	logger                      zerolog.Logger
	iamClient                   iam.IAMClient
	iamManager                  iam.IAMManager // The orchestrator now uses the manager.
	iamProjectManager           *iam.IAMProjectManager
	planner                     *iam.RolePlanner
	createdServiceAccountEmails []string
}

// NewIAMOrchestrator creates a new orchestrator focused solely on IAM.
func NewIAMOrchestrator(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*IAMOrchestrator, error) {
	var iamClient iam.IAMClient
	var err error

	// This logic for creating a test or real client remains the same.
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

	// The orchestrator now creates the manager, which has the full architecture context.
	iamManager, err := iam.NewIAMManager(iamClient, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create iam manager: %w", err)
	}

	return &IAMOrchestrator{
		arch:                        arch,
		logger:                      logger.With().Str("component", "IAMOrchestrator").Logger(),
		iamClient:                   iamClient,
		iamManager:                  iamManager, // Store the manager.
		iamProjectManager:           iamProjectManager,
		planner:                     iam.NewRolePlanner(logger),
		createdServiceAccountEmails: make([]string, 0),
	}, nil
}

// ApplyIAMForDataflow is the new method that correctly handles IAM for a single dataflow.
// It replaces the old SetupDataflowIAM and absorbs the logic from the deleted DeploymentManager.
func (o *IAMOrchestrator) ApplyIAMForDataflow(ctx context.Context, dataflowName string) (map[string]string, error) {
	dataflow, ok := o.arch.Dataflows[dataflowName]
	if !ok {
		return nil, fmt.Errorf("dataflow '%s' not found in architecture", dataflowName)
	}

	o.logger.Info().Str("dataflow", dataflowName).Msg("Applying IAM for all services in dataflow.")
	serviceEmails := make(map[string]string)

	// Iterate over all services defined in this dataflow.
	for serviceName, serviceSpec := range dataflow.Services {
		// Call the IAMManager to apply the policies for this specific service.
		err := o.iamManager.ApplyIAMForService(ctx, dataflow, serviceName)
		if err != nil {
			return nil, fmt.Errorf("failed to apply IAM for service '%s' in dataflow '%s': %w", serviceName, dataflowName, err)
		}

		// After applying, ensure we track the service account email for deployment.
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

// --- (SetupServiceDirectorIAM and Teardown methods remain largely the same) ---

// SetupServiceDirectorIAM ensures the ServiceDirector's SA exists and has project-level roles.
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
		return nil, fmt.Errorf("failed to ensure ServiceDirector service account exists: %w", err)
	}
	o.logger.Info().Str("email", saEmail).Msg("ServiceDirector service account is ready.")
	o.createdServiceAccountEmails = append(o.createdServiceAccountEmails, saEmail)
	serviceEmails[serviceName] = saEmail

	member := "serviceAccount:" + saEmail
	for _, role := range requiredRoles {
		o.logger.Info().Str("role", role).Str("member", member).Msg("Granting project-level role to ServiceDirector service account.")
		if err := o.iamProjectManager.AddProjectIAMBinding(ctx, member, role); err != nil {
			return nil, fmt.Errorf("failed to grant project role '%s' to ServiceDirector SA: %w", role, err)
		}
	}

	// This logic for Cloud Build permissions remains necessary.
	// A production implementation might make this more configurable.
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

// Teardown cleans up IAM resources and closes the client.
func (o *IAMOrchestrator) Teardown(ctx context.Context) error {
	o.logger.Info().Int("created", len(o.createdServiceAccountEmails)).Msg("Starting IAMOrchestrator cleanup...")

	for _, email := range o.createdServiceAccountEmails {
		if err := o.iamClient.DeleteServiceAccount(ctx, email); err != nil {
			o.logger.Error().Err(err).Str("email", email).Msg("Failed during service account cleanup")
		}
	}

	if o.iamClient != nil {
		if err := o.iamClient.Close(); err != nil {
			o.logger.Error().Err(err).Msg("Failed to close IAM client")
		}
	}

	o.logger.Info().Msg("IAMOrchestrator cleanup finished.")
	return nil
}

func (o *IAMOrchestrator) GetProjectNumber(ctx context.Context, projectID string) (string, error) {
	getProjectReq := &resourcemanagerpb.GetProjectRequest{
		Name: fmt.Sprintf("projects/%s", projectID),
	}

	rmClient, err := resourcemanager.NewProjectsClient(ctx)

	project, err := rmClient.GetProject(ctx, getProjectReq)
	if err != nil {
		return "", err
	}
	projectNumber := project.Name[len("projects/"):]
	return projectNumber, nil
}
