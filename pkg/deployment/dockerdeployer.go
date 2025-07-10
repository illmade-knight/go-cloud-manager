package deployment

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/deployment/docker"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DockerCloudRunDeployer implements the ContainerDeployer interface using a
// local Docker build/push followed by a Cloud Run Admin API call.
type DockerCloudRunDeployer struct {
	projectID     string
	defaultRegion string
	fileWriter    *docker.FileWriter // From your provided code
	imager        *docker.Imager     // From your provided code
	runService    *run.Service       // The real Cloud Run client
	logger        zerolog.Logger
}

// NewDockerCloudRunDeployer creates a fully initialized deployer.
// It now accepts a mainPackagePath to correctly initialize the FileWriter.
func NewDockerCloudRunDeployer(
	ctx context.Context,
	projectID, defaultRegion, mainPackagePath string,
	logger zerolog.Logger,
	opts ...option.ClientOption,
) (*DockerCloudRunDeployer, error) {

	// The Cloud Run Admin API endpoint is regional.
	endpoint := fmt.Sprintf("%s-run.googleapis.com:443", defaultRegion)
	runOpts := append(opts, option.WithEndpoint(endpoint))

	runService, err := run.NewService(ctx, runOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloud Run service client: %w", err)
	}

	// Use the new constructor for FileWriter
	fileWriter := docker.NewFileWriter(logger, mainPackagePath, "") // Assuming default registry isn't needed here

	// Create the imager component
	imager := docker.NewDockerImager(logger)

	return &DockerCloudRunDeployer{
		projectID:     projectID,
		defaultRegion: defaultRegion,
		fileWriter:    fileWriter,
		imager:        imager,
		runService:    runService,
		logger:        logger.With().Str("component", "DockerCloudRunDeployer").Logger(),
	}, nil
}

// Deploy orchestrates the full build, push, and deploy workflow.
func (d *DockerCloudRunDeployer) Deploy(ctx context.Context, serviceName, serviceAccountEmail string, spec servicemanager.DeploymentSpec) error {
	// Assume source code is in a conventional path, e.g., cmd/<service-name>
	sourcePath := filepath.Join("cmd", serviceName)
	dockerfilePath := filepath.Join(sourcePath, "Dockerfile")

	imageTag := spec.Image // Use the image tag from the deployment spec

	// --- Step 1: Generate Dockerfile (if needed) ---
	exists, err := d.fileWriter.DockerfileExists(dockerfilePath)
	if err != nil {
		return err
	}
	if !exists {
		if err := d.fileWriter.GenerateAndWriteDockerfile(serviceName, dockerfilePath); err != nil {
			return err
		}
	}

	// --- Step 2: Build the image ---
	if err := d.imager.BuildImage(ctx, dockerfilePath, ".", imageTag); err != nil {
		return err
	}

	// --- Step 3: Push the image to the registry ---
	if err := d.imager.PushImage(ctx, imageTag); err != nil {
		return err
	}

	d.logger.Info().Str("image", imageTag).Msg("Image pushed successfully. Deploying to Cloud Run...")

	// --- Step 4: Deploy the pushed image to Cloud Run ---
	err = d.deployToCloudRun(ctx, serviceName, serviceAccountEmail, spec)
	if err != nil {
		return fmt.Errorf("failed to deploy image to Cloud Run: %w", err)
	}

	d.logger.Info().Str("service", serviceName).Msg("Service deployed successfully to Cloud Run.")
	return nil
}

// deployToCloudRun uses the native client to create or update a Cloud Run service.
func (d *DockerCloudRunDeployer) deployToCloudRun(ctx context.Context, serviceName, saEmail string, spec servicemanager.DeploymentSpec) error {
	parent := fmt.Sprintf("projects/%s/locations/%s", d.projectID, d.defaultRegion)
	fullServiceName := fmt.Sprintf("%s/services/%s", parent, serviceName)

	desiredService := &run.GoogleCloudRunV2Service{
		Template: &run.GoogleCloudRunV2RevisionTemplate{
			ServiceAccount: saEmail,
			Containers: []*run.GoogleCloudRunV2Container{
				{
					Image: spec.Image,
					Resources: &run.GoogleCloudRunV2ResourceRequirements{
						Limits: map[string]string{
							"cpu":    spec.CPU,
							"memory": spec.Memory,
						},
					},
				},
			},
			Scaling: &run.GoogleCloudRunV2RevisionScaling{
				MinInstanceCount: spec.MinInstances,
				MaxInstanceCount: spec.MaxInstances,
			},
		},
	}

	var envVars []*run.GoogleCloudRunV2EnvVar
	for k, v := range spec.EnvironmentVars {
		envVars = append(envVars, &run.GoogleCloudRunV2EnvVar{Name: k, Value: v})
	}
	desiredService.Template.Containers[0].Env = envVars

	_, err := d.runService.Projects.Locations.Services.Get(fullServiceName).Context(ctx).Do()

	var op *run.GoogleLongrunningOperation
	if err != nil {
		if e, ok := status.FromError(err); ok && e.Code() == codes.NotFound {
			d.logger.Info().Str("service", serviceName).Msg("Service not found. Creating new Cloud Run service...")
			op, err = d.runService.Projects.Locations.Services.Create(parent, desiredService).ServiceId(serviceName).Context(ctx).Do()
		} else {
			return fmt.Errorf("failed to get status of existing Cloud Run service: %w", err)
		}
	} else {
		d.logger.Info().Str("service", serviceName).Msg("Service found. Updating existing Cloud Run service...")
		op, err = d.runService.Projects.Locations.Services.Patch(fullServiceName, desiredService).Context(ctx).Do()
	}

	if err != nil {
		return fmt.Errorf("failed to trigger Cloud Run create/update operation: %w", err)
	}

	d.logger.Info().Str("operation", op.Name).Msg("Waiting for Cloud Run operation to complete...")
	for {
		getOp, err := d.runService.Projects.Locations.Operations.Get(op.Name).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("failed to poll Cloud Run operation status: %w", err)
		}
		if getOp.Done {
			if getOp.Error != nil {
				return fmt.Errorf("Cloud Run operation failed with status: %s", getOp.Error.Message)
			}
			break
		}
		time.Sleep(5 * time.Second)
	}

	return nil
}

// Teardown is a placeholder for the teardown logic.
func (d *DockerCloudRunDeployer) Teardown(ctx context.Context, serviceName string) error {
	// Implementation would use d.runService to delete the Cloud Run service.
	return nil
}
