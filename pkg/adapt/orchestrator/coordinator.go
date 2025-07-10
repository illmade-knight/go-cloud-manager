// deployer/orchestrator/orchestrator.go
package orchestrator

import (
	"context"
	"github.com/illmade-knight/go-iot-dataflows/deployment/cloudrun"
	"github.com/illmade-knight/go-iot-dataflows/deployment/config"
	"github.com/illmade-knight/go-iot-dataflows/deployment/docker"
	"github.com/illmade-knight/go-iot-dataflows/deployment/iam"
	"github.com/illmade-knight/go-iot/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// Coordinator orchestrates the end-to-end deployment process of services.
type Coordinator struct {
	deployer       *Deployer
	IAMProvisioner *IAMProvisioner
	logger         zerolog.Logger
}

// NewCoordinater creates a new Coordinator instance.
func NewCoordinater(
	cloudRunClient *cloudrun.Client,
	iamClient *iam.Client,
	logger zerolog.Logger,
) *Coordinator {
	coordinatorLogger := logger.With().Str("component", "coordinator").Logger()

	dockerLogger := logger.With().Str("component", "dockerBuilder").Logger()
	imager := docker.NewDockerImager(dockerLogger)

	deploymentLogger := logger.With().Str("component", "deployer").Logger()
	deployer := &Deployer{
		dfw:         docker.FileWriter{},
		di:          imager,
		cloudClient: cloudRunClient,
		logger:      deploymentLogger,
	}

	iamLogger := logger.With().Str("component", "iam").Logger()
	provisioner := &IAMProvisioner{
		logger:    iamLogger,
		iamClient: iamClient,
	}

	return &Coordinator{
		deployer:       deployer,
		IAMProvisioner: provisioner,
		logger:         coordinatorLogger,
	}
}

func (d *Coordinator) CoordinateDeployment(ctx context.Context, servicesConfig *servicemanager.TopLevelConfig, deployment *config.DeployerConfig) error {
	err := d.IAMProvisioner.provisionIAM(ctx, servicesConfig)
	if err != nil {
		return err
	}
	//TODO coordinate deployment

	return nil
}

func (d *Coordinator) SerialDeploy(ctx context.Context, servicesConfig *servicemanager.TopLevelConfig, deployment ServiceDeploymentConfig) error {
	err := d.IAMProvisioner.provisionIAM(ctx, servicesConfig)
	if err != nil {
		return err
	}
	d.logger.Info().Msg("iam finished")

	serviceUri, err := d.deployer.SerialDeployService(ctx, deployment)
	if err != nil {
		return err
	}
	d.logger.Info().Str("serviceUri", serviceUri).Msg("Deployed service")

	return nil
}

func (d *Coordinator) CoordinateTeardown(ctx context.Context, servicesConfig *servicemanager.TopLevelConfig) error {
	//TODO coordinate teardown

	return nil
}

// TeardownDataflow orchestrates the tearing down of ephemeral services and resources.
func (d *Coordinator) TeardownDataflow(ctx context.Context, projectID string, dataflow servicemanager.ResourceGroup, teardownAll bool) error {
	// REMOVE configured IAM for resources
	if dataflow.Lifecycle != nil && (teardownAll || dataflow.Lifecycle.Strategy == servicemanager.LifecycleStrategyEphemeral) {
		d.logger.Info().Str("dataflow", dataflow.Name).Msg("Tearing down dataflow resources and services.")
		err := d.IAMProvisioner.TeardownIAM(ctx, projectID, dataflow)
		if err != nil {
			return err
		}
	}
	//TODO teardown the dataflow services
	d.logger.Info().Msg("Teardown orchestration completed.")
	return nil
}
