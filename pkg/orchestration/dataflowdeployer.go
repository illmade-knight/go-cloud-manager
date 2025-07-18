package orchestration

import (
	"context"
	"fmt"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"

	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// DataflowDeployer is responsible for deploying all services within a specific dataflow.
type DataflowDeployer struct {
	cfg      Config // Uses the same config as the orchestrator
	logger   zerolog.Logger
	deployer *deployment.CloudBuildDeployer
	iam      iam.IAMClient
}

// NewDataflowDeployer creates a new deployer for dataflow services.
func NewDataflowDeployer(cfg Config, logger zerolog.Logger, deployer *deployment.CloudBuildDeployer, iam iam.IAMClient) *DataflowDeployer {
	return &DataflowDeployer{
		cfg:      cfg,
		logger:   logger.With().Str("component", "DataflowDeployer").Logger(),
		deployer: deployer,
		iam:      iam,
	}
}

// DeployServices loops through the services defined in a dataflow and deploys them.
func (dd *DataflowDeployer) DeployServices(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName, directorURL string) error {
	dd.logger.Info().Str("dataflow", dataflowName).Msg("Beginning deployment of all application services...")

	dataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow '%s' not found in architecture definition", dataflowName)
	}

	if len(dataflow.Services) == 0 {
		dd.logger.Info().Str("dataflow", dataflowName).Msg("No application services defined in dataflow, deployment step complete.")
		return nil
	}

	for serviceName, serviceSpec := range dataflow.Services {
		dd.logger.Info().Str("service", serviceName).Msg("Starting deployment...")

		// Ensure the runtime service account for this service exists.
		runtimeSAEmail, err := dd.iam.EnsureServiceAccountExists(ctx, serviceSpec.ServiceAccount)
		if err != nil {
			return fmt.Errorf("failed to ensure service account for service '%s': %w", serviceName, err)
		}

		// Use the deployment spec from the YAML file.
		deploymentSpec := serviceSpec.Deployment
		if deploymentSpec == nil {
			dd.logger.Warn().Str("service", serviceName).Msg("Service has no deployment spec, skipping.")
			continue
		}

		// Dynamically generate the image tag.
		deploymentSpec.Image = fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:%s", dd.cfg.Region, dd.cfg.ProjectID, dd.cfg.ImageRepo, serviceName, uuid.New().String()[:8])

		// Inject the Director's URL and the Project ID as environment variables.
		if deploymentSpec.EnvironmentVars == nil {
			deploymentSpec.EnvironmentVars = make(map[string]string)
		}
		deploymentSpec.EnvironmentVars["SERVICE_DIRECTOR_URL"] = directorURL
		deploymentSpec.EnvironmentVars["PROJECT_ID"] = dd.cfg.ProjectID

		// Use the deployer to perform the full build and deploy.
		_, err = dd.deployer.Deploy(ctx, serviceName, "", runtimeSAEmail, *deploymentSpec)
		if err != nil {
			return fmt.Errorf("failed to deploy service '%s': %w", serviceName, err)
		}
		dd.logger.Info().Str("service", serviceName).Msg("Deployment successful.")
	}

	return nil
}
