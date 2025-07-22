package orchestration

import (
	"context"
	"fmt"
	"os"

	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IAMOrchestrator is responsible for planning and applying all IAM policies for an architecture.
type IAMOrchestrator struct {
	arch      *servicemanager.MicroserviceArchitecture
	logger    zerolog.Logger
	iamClient iam.IAMClient
	// This client is specifically for project-level roles, which have a different API.
	iamProjectManager *iam.IAMProjectManager
	planner           *iam.RolePlanner
	// Tracks all created SAs for teardown.
	createdServiceAccountEmails []string
}

// NewIAMOrchestrator creates a new orchestrator focused solely on IAM.
func NewIAMOrchestrator(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) (*IAMOrchestrator, error) {
	var iamClient iam.IAMClient
	var err error

	if os.Getenv("TEST_SA_POOL_MODE") == "true" {
		prefix := os.Getenv("TEST_SA_POOL_PREFIX")
		if prefix == "" {
			prefix = "test-pool-sa-"
		}
		logger.Warn().Str("prefix", prefix).Msg("TEST_SA_POOL_MODE enabled. Using pooled IAM client.")
		// The test client needs its own concrete type to access Close()
		testClient, err := iam.NewTestIAMClient(ctx, arch.ProjectID, logger, prefix)
		if err != nil {
			return nil, fmt.Errorf("failed to create test iamClient: %w", err)
		}
		iamClient = testClient
	} else {
		logger.Info().Msg("Running in standard mode. Using real IAM client.")
		iamClient, err = iam.NewGoogleIAMClient(ctx, arch.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("failed to create iamClient for orchestrator: %w", err)
		}
	}

	iamProjectManager, err := iam.NewIAMProjectManager(ctx, arch.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create iam project manager: %w", err)
	}

	planner := iam.NewRolePlanner(logger)

	return &IAMOrchestrator{
		arch:                        arch,
		logger:                      logger.With().Str("component", "IAMOrchestrator").Logger(),
		iamClient:                   iamClient,
		iamProjectManager:           iamProjectManager,
		planner:                     planner,
		createdServiceAccountEmails: make([]string, 0),
	}, nil
}

// SetupServiceDirectorIAM ensures the ServiceDirector's SA exists and has project-level roles.
// It now returns a map containing the service name and its corresponding SA email.
func (o *IAMOrchestrator) SetupServiceDirectorIAM(ctx context.Context) (map[string]string, error) {
	o.logger.Info().Msg("Starting ServiceDirector IAM setup...")
	serviceEmails := make(map[string]string)

	requiredRoles, err := o.planner.PlanRolesForServiceDirector(ctx, o.arch)
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

	o.logger.Info().Msg("Granting 'Service Account User' role to Cloud Build SA...")
	rm, err := deployment.NewResourceManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create resource manager to find project number: %w", err)
	}
	defer rm.Close()
	projectNumber, err := rm.GetProjectNumber(ctx, o.arch.ProjectID)
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

// SetupDataflowIAM ensures SAs exist for all dataflow services and grants them resource-level permissions.
// It now returns a map of all dataflow service names to their SA emails.
func (o *IAMOrchestrator) SetupDataflowIAM(ctx context.Context) (map[string]string, error) {
	o.logger.Info().Msg("Starting Dataflow application services IAM setup...")
	serviceEmails := make(map[string]string)

	plan, err := o.planner.PlanRolesForApplicationServices(ctx, o.arch)
	if err != nil {
		return nil, fmt.Errorf("failed to plan application service IAM roles: %w", err)
	}

	for saName, bindings := range plan {
		saEmail, err := o.iamClient.EnsureServiceAccountExists(ctx, saName)
		if err != nil {
			return nil, fmt.Errorf("failed to ensure service account %s: %w", saName, err)
		}
		o.createdServiceAccountEmails = append(o.createdServiceAccountEmails, saEmail)

		// Find the service name that corresponds to this service account
		var serviceName string
		for _, df := range o.arch.Dataflows {
			for name, spec := range df.Services {
				if spec.ServiceAccount == saName {
					serviceName = name
					break
				}
			}
		}
		if serviceName != "" {
			serviceEmails[serviceName] = saEmail
		}

		member := "serviceAccount:" + saEmail
		o.logger.Info().Str("service_account", saEmail).Int("binding_count", len(bindings)).Msg("Applying IAM bindings...")
		for _, binding := range bindings {
			err := o.iamClient.AddResourceIAMBinding(ctx, binding.ResourceType, binding.ResourceID, binding.Role, member)
			if err != nil {
				return nil, fmt.Errorf("failed to apply binding for SA %s on resource %s: %w", saEmail, binding.ResourceID, err)
			}
		}
	}

	o.logger.Info().Msg("Dataflow application services IAM setup complete.")
	return serviceEmails, nil
}

// Teardown cleans up IAM resources and closes the client.
func (o *IAMOrchestrator) Teardown(ctx context.Context) error {
	o.logger.Info().Msg("Starting IAMOrchestrator cleanup...")

	// In test mode, this returns the SA to the pool. In standard mode, it deletes it.
	for _, email := range o.createdServiceAccountEmails {
		if err := o.iamClient.DeleteServiceAccount(ctx, email); err != nil {
			o.logger.Error().Err(err).Str("email", email).Msg("Failed during service account cleanup")
		}
	}

	// Close the client. For the test client, this cleans roles from the pool.
	if o.iamClient != nil {
		if testClient, ok := o.iamClient.(*iam.TestIAMClient); ok {
			if err := testClient.Close(); err != nil {
				o.logger.Error().Err(err).Msg("Failed to close and clean test IAM client")
			}
		} else if realClient, ok := o.iamClient.(*iam.GoogleIAMClient); ok {
			if err := realClient.Close(); err != nil {
				o.logger.Error().Err(err).Msg("Failed to close real IAM client")
			}
		}
	}

	o.logger.Info().Msg("IAMOrchestrator cleanup finished.")
	return nil
}
