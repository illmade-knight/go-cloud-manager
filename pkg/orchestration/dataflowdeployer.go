package orchestration

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// DataflowDeployer is responsible for deploying all services within a specific dataflow.
type DataflowDeployer struct {
	arch      *servicemanager.MicroserviceArchitecture // Holds the entire plan
	logger    zerolog.Logger
	deployer  *deployment.CloudBuildDeployer
	iamClient iam.IAMClient
}

// NewDataflowDeployer creates a new deployer for dataflow services.
// It now creates its own IAM client.
func NewDataflowDeployer(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, deployer *deployment.CloudBuildDeployer) (*DataflowDeployer, error) {
	iamClient, err := iam.NewGoogleIAMClient(ctx, arch.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create iamClient client for dataflow deployer: %w", err)
	}

	return &DataflowDeployer{
		arch:      arch,
		logger:    logger.With().Str("component", "DataflowDeployer").Logger(),
		deployer:  deployer,
		iamClient: iamClient,
	}, nil
}

// DeployServices loops through the services defined in a dataflow and deploys them.
func (dd *DataflowDeployer) DeployServices(ctx context.Context, dataflowName, directorURL string) error {
	dd.logger.Info().Str("dataflow", dataflowName).Msg("Beginning deployment of all application services...")

	dataflow, ok := dd.arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow '%s' not found in architecture definition", dataflowName)
	}

	if len(dataflow.Services) == 0 {
		dd.logger.Info().Str("dataflow", dataflowName).Msg("No application services defined in dataflow, deployment step complete.")
		return nil
	}

	for serviceName, serviceSpec := range dataflow.Services {
		dd.logger.Info().Str("service", serviceName).Msg("Starting deployment...")

		runtimeSAEmail, err := dd.iamClient.EnsureServiceAccountExists(ctx, serviceSpec.ServiceAccount)
		if err != nil {
			return fmt.Errorf("failed to ensure service account for service '%s': %w", serviceName, err)
		}

		deploymentSpec := serviceSpec.Deployment
		if deploymentSpec == nil {
			dd.logger.Warn().Str("service", serviceName).Msg("Service has no deployment spec, skipping.")
			continue
		}

		// Now, Region and ImageRepo come from the spec itself.
		deploymentSpec.Image = fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:%s", deploymentSpec.Region, dd.arch.ProjectID, deploymentSpec.ImageRepo, serviceName, uuid.New().String()[:8])

		if deploymentSpec.EnvironmentVars == nil {
			deploymentSpec.EnvironmentVars = make(map[string]string)
		}
		deploymentSpec.EnvironmentVars["SERVICE_DIRECTOR_URL"] = directorURL
		deploymentSpec.EnvironmentVars["PROJECT_ID"] = dd.arch.ProjectID

		_, err = dd.deployer.Deploy(ctx, serviceName, "", runtimeSAEmail, *deploymentSpec)
		if err != nil {
			return fmt.Errorf("failed to deploy service '%s': %w", serviceName, err)
		}
		dd.logger.Info().Str("service", serviceName).Msg("Deployment successful.")
	}

	return nil
}
