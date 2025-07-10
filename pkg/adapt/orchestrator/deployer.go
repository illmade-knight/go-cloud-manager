package orchestrator

import (
	"context"
	"fmt"
	"github.com/illmade-knight/go-iot-dataflows/deployment/cloudrun"
	"github.com/illmade-knight/go-iot-dataflows/deployment/config"
	"github.com/illmade-knight/go-iot-dataflows/deployment/docker"
	"github.com/rs/zerolog"
)

// ServiceDeploymentConfig holds deployment-specific configuration for a single service.
type ServiceDeploymentConfig struct {
	ServiceName    string
	DockerConfig   DockerDeploymentConfig
	CloudrunConfig config.CloudRunSetup
}

// DockerDeploymentConfig holds Docker-related configuration for building and pushing images.
type DockerDeploymentConfig struct {
	Dockerfile string
	SourcePath string
	ImageTag   string
}

// Deployer orchestrates the deployment of services, including Docker image building/pushing and Cloud Run deployments.
type Deployer struct {
	dfw         docker.FileWriter
	di          *docker.Imager
	cloudClient *cloudrun.Client
	logger      zerolog.Logger
}

func (d *Deployer) deployDataflow(ctx context.Context, serviceManagerURL string, serviceSpecs []ServiceDeploymentConfig) error {
	for _, sc := range serviceSpecs {
		exists, err := d.dfw.DockerfileExists(sc.DockerConfig.Dockerfile)
		if err != nil && !exists {
			err := d.dfw.GenerateAndWriteDockerfile(sc.ServiceName, sc.DockerConfig.Dockerfile)
			if err != nil {
				d.logger.Info().Str("name", sc.ServiceName).Str("Dockerfile", sc.DockerConfig.Dockerfile).Msg("failed to create Dockerfile")
				return fmt.Errorf("failed to create Dockerfile %s: %w", sc.ServiceName, err)
			}
			d.logger.Info().Msg("generated Dockerfile for serviceManager")
		}

		err = d.di.BuildImage(ctx, sc.DockerConfig.Dockerfile, sc.DockerConfig.SourcePath, sc.DockerConfig.ImageTag)
		if err != nil {
			d.logger.Info().Str("name", sc.ServiceName).Msg("failed to build image")
			return fmt.Errorf("failed to build image %s: %w", sc.ServiceName, err)
		}
		d.logger.Info().Msg("built image for serviceManager")
	}

	for _, sc := range serviceSpecs {
		err := d.di.BuildImage(ctx, sc.DockerConfig.Dockerfile, sc.DockerConfig.SourcePath, sc.DockerConfig.ImageTag)
		if err != nil {
			d.logger.Info().Str("name", sc.ServiceName).Msg("failed to build image")
			return fmt.Errorf("failed to build image %s: %w", sc.ServiceName, err)
		}
		d.logger.Info().Msg("built image for serviceManager")

		err = d.di.PushImage(ctx, sc.DockerConfig.ImageTag)
		if err != nil {
			d.logger.Info().Str("name", sc.ServiceName).Msg("failed to push image")
			return fmt.Errorf("failed to push image %s: %w", sc.ServiceName, err)
		}
		d.logger.Info().Msg("pushed image for serviceManager")

		d.logger.Info().Str("service", sc.ServiceName).Msg("Deploying to Cloud Run.")
		service, err := d.cloudClient.DeployService(ctx, sc.ServiceName, sc.DockerConfig.ImageTag, sc.CloudrunConfig)
		if err != nil {
			return fmt.Errorf("failed to deploy service %s to Cloud Run: %w", sc.ServiceName, err)
		}
		d.logger.Info().Str("service", sc.ServiceName).Str("url", service.Uri).Msg("Service deployed successfully to Cloud Run.")
	}

	return nil
}

// SerialDeployService we have 2 options when deploying services - we can carry out the deployment serially, first create
// Dockerfile, build, push, deploy to cloud run - the other option is
func (d *Deployer) SerialDeployService(ctx context.Context, sc ServiceDeploymentConfig) (string, error) {

	// Initialize directorServiceURL; it currently captures the URL of the last processed service.
	var directorServiceURL string = ""

	exists, err := d.dfw.DockerfileExists(sc.DockerConfig.Dockerfile)

	if err != nil && !exists {
		err := d.dfw.GenerateAndWriteDockerfile(sc.ServiceName, sc.DockerConfig.Dockerfile)
		if err != nil {
			d.logger.Info().Str("name", sc.ServiceName).Str("Dockerfile", sc.DockerConfig.Dockerfile).Msg("failed to create Dockerfile")
			return "", fmt.Errorf("failed to create Dockerfile %s: %w", sc.ServiceName, err)
		}
		d.logger.Info().Msg("generated Dockerfile for serviceManager")
	}

	err = d.di.BuildImage(ctx, sc.DockerConfig.Dockerfile, sc.DockerConfig.SourcePath, sc.DockerConfig.ImageTag)
	if err != nil {
		d.logger.Info().Str("name", sc.ServiceName).Msg("failed to build image")
		return "", fmt.Errorf("failed to build image %s: %w", sc.ServiceName, err)
	}
	d.logger.Info().Msg("built image for serviceManager")

	err = d.di.BuildImage(ctx, sc.DockerConfig.Dockerfile, sc.DockerConfig.SourcePath, sc.DockerConfig.ImageTag)
	if err != nil {
		d.logger.Info().Str("name", sc.ServiceName).Msg("failed to build image")
		return "", fmt.Errorf("failed to build image %s: %w", sc.ServiceName, err)
	}
	d.logger.Info().Msg("built image for serviceManager")

	err = d.di.PushImage(ctx, sc.DockerConfig.ImageTag)
	if err != nil {
		d.logger.Info().Str("name", sc.ServiceName).Msg("failed to push image")
		return "", fmt.Errorf("failed to push image %s: %w", sc.ServiceName, err)
	}
	d.logger.Info().Msg("pushed image for serviceManager")

	d.logger.Info().Str("service", sc.ServiceName).Msg("Deploying to Cloud Run.")

	service, err := d.cloudClient.DeployService(ctx, sc.ServiceName, sc.DockerConfig.ImageTag, sc.CloudrunConfig)
	if err != nil {
		d.logger.Info().Str("name", sc.ServiceName).Msg("failed to deploy service")
		return "", fmt.Errorf("failed to deploy service %s: %w", sc.ServiceName, err)
	}

	d.logger.Info().Str("service", sc.ServiceName).Str("url", service.Uri).Msg("Cloud Run service deployed.")
	directorServiceURL = service.Uri // FIX: Changed 'service.Url' to 'service.Uri'

	return directorServiceURL, nil
}

// BuildAndPushImages builds and pushes Docker images for a slice of ServiceDeploymentConfig.
// This function encapsulates the Docker-related operations.
func (d *Deployer) BuildAndPushImages(ctx context.Context, serviceSpecs []ServiceDeploymentConfig) error {
	d.logger.Info().Msg("Starting Docker image build and push for all services.")
	for _, sc := range serviceSpecs {
		d.logger.Info().Str("service", sc.ServiceName).Msg("Processing Docker image for service.")

		exists, err := d.dfw.DockerfileExists(sc.DockerConfig.Dockerfile)
		if err != nil && !exists { // Check err for cases other than 'not exist'
			err := d.dfw.GenerateAndWriteDockerfile(sc.ServiceName, sc.DockerConfig.Dockerfile)
			if err != nil {
				d.logger.Error().Err(err).Str("name", sc.ServiceName).Str("Dockerfile", sc.DockerConfig.Dockerfile).Msg("failed to create Dockerfile")
				return fmt.Errorf("failed to create Dockerfile for %s: %w", sc.ServiceName, err)
			}
			d.logger.Info().Str("name", sc.ServiceName).Msg("generated Dockerfile")
		} else if err != nil {
			return fmt.Errorf("failed to check Dockerfile existence for %s: %w", sc.ServiceName, err)
		}

		err = d.di.BuildImage(ctx, sc.DockerConfig.Dockerfile, sc.DockerConfig.SourcePath, sc.DockerConfig.ImageTag)
		if err != nil {
			d.logger.Error().Err(err).Str("name", sc.ServiceName).Msg("failed to build image")
			return fmt.Errorf("failed to build image %s: %w", sc.ServiceName, err)
		}
		d.logger.Info().Str("name", sc.ServiceName).Msg("built image")

		err = d.di.PushImage(ctx, sc.DockerConfig.ImageTag)
		if err != nil {
			d.logger.Error().Err(err).Str("name", sc.ServiceName).Msg("failed to push image")
			return fmt.Errorf("failed to push image %s: %w", sc.ServiceName, err)
		}
		d.logger.Info().Str("name", sc.ServiceName).Msg("pushed image")
	}
	d.logger.Info().Msg("Completed Docker image build and push for all services.")
	return nil
}

// DeployCloudRunServices deploys a slice of ServiceDeploymentConfig to Cloud Run.
// This function encapsulates the Cloud Run deployment operations.
func (d *Deployer) DeployCloudRunServices(ctx context.Context, serviceSpecs []ServiceDeploymentConfig) error {
	d.logger.Info().Msg("Starting Cloud Run deployment for all services.")
	for _, sc := range serviceSpecs {
		d.logger.Info().Str("service", sc.ServiceName).Msg("Deploying service to Cloud Run.")
		service, err := d.cloudClient.DeployService(ctx, sc.ServiceName, sc.DockerConfig.ImageTag, sc.CloudrunConfig)
		if err != nil {
			d.logger.Error().Err(err).Str("name", sc.ServiceName).Msg("failed to deploy service to Cloud Run")
			return fmt.Errorf("failed to deploy service %s to Cloud Run: %w", sc.ServiceName, err)
		}
		d.logger.Info().Str("service", sc.ServiceName).Str("url", service.Uri).Msg("Cloud Run service deployed.")
	}
	d.logger.Info().Msg("Completed Cloud Run deployment for all services.")
	return nil
}
