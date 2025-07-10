package deployment

import (
	"context"
	"fmt"
	"github.com/googleapis/gax-go/v2"
	"time"

	"cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/storage"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
)

// CloudBuildDeployer now uses both the Cloud Build and Cloud Run clients.
type CloudBuildDeployer struct {
	projectID        string
	defaultRegion    string
	sourceBucket     string
	storageClient    *storage.Client
	cloudbuildClient *cloudbuild.Client // The client for triggering builds
	runService       *run.Service       // The client for deploying the final image
	logger           zerolog.Logger
}

func NewCloudBuildDeployer(ctx context.Context, projectID, defaultRegion, sourceBucket string, logger zerolog.Logger, opts ...option.ClientOption) (*CloudBuildDeployer, error) {
	// ... (Initialization for storageClient and runService remains the same)

	buildClient, err := cloudbuild.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloud build client: %w", err)
	}

	return &CloudBuildDeployer{
		projectID:        projectID,
		defaultRegion:    defaultRegion,
		sourceBucket:     sourceBucket,
		storageClient:    storageClient,
		cloudbuildClient: buildClient,
		runService:       runService,
		logger:           logger.With().Str("component", "CloudBuildDeployer").Logger(),
	}, nil
}

// Deploy orchestrates the upload, build, and deploy workflow.
func (d *CloudBuildDeployer) Deploy(ctx context.Context, serviceName, serviceAccountEmail string, spec servicemanager.DeploymentSpec) error {
	d.logger.Info().Str("service", serviceName).Msg("Starting native cloud build and deploy workflow...")

	// Step 1: Package and Upload Source Code to GCS (conceptual)
	sourceObject, err := d.uploadSourceToGCS(ctx, serviceName)
	if err != nil {
		return err
	}

	// Step 2: Trigger a build on Cloud Build using the uploaded source.
	imageTag := spec.Image // e.g., "us-central1-docker.pkg.dev/my-proj/services/my-service:latest"
	err = d.triggerCloudBuild(ctx, sourceObject, imageTag)
	if err != nil {
		return fmt.Errorf("Cloud Build failed for service '%s': %w", serviceName, err)
	}
	d.logger.Info().Str("image", imageTag).Msg("Cloud Build successful. Image is ready.")

	// Step 3: Deploy the newly built image to Cloud Run.
	err = d.deployToCloudRun(ctx, serviceName, serviceAccountEmail, spec)
	if err != nil {
		return fmt.Errorf("Cloud Run deployment failed for service '%s': %w", serviceName, err)
	}

	return nil
}

// triggerCloudBuild uses the cloudbuild/apiv1 client to build a container from source.
func (d *CloudBuildDeployer) triggerCloudBuild(ctx context.Context, gcsSourceObject, outputImageTag string) error {
	req := &cloudbuildpb.CreateBuildRequest{
		ProjectId: d.projectID,
		Build: &cloudbuildpb.Build{
			Source: &cloudbuildpb.Source{
				Source: &cloudbuildpb.Source_StorageSource{
					StorageSource: &cloudbuildpb.StorageSource{
						Bucket: d.sourceBucket,
						Object: gcsSourceObject,
					},
				},
			},
			// Use Google's official builders. This step uses Docker to build.
			// Alternatively, you could use buildpacks here if no Dockerfile is present.
			Steps: []*cloudbuildpb.BuildStep{
				{
					Name: "gcr.io/cloud-builders/docker",
					Args: []string{"build", "-t", outputImageTag, "."},
				},
			},
			Images: []string{outputImageTag},
		},
	}

	op, err := d.cloudbuildClient.CreateBuild(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create cloud build: %w", err)
	}

	md, err := op.Metadata()
	if err != nil {
		return fmt.Errorf("failed to get metadata: %w", err)
	}

	d.logger.Info().Str("build_id", md.GetBuild().GetId()).Msg("Waiting for Cloud Build job to complete...")
	// In a real implementation, you would poll the operation until it's done.
	// For brevity, we'll assume it succeeds.
	opts := []gax.CallOption{}
	build, err := op.Poll(ctx, opts...)
	if err != nil {
		return err
	}
	d.logger.Info().Str("build_id", build.GetId()).Msg("Cloud Build job completed...")

	return nil
}

// deployToCloudRun is now simpler. It only needs to deploy a specific image tag.
func (d *CloudBuildDeployer) deployToCloudRun(ctx context.Context, serviceName, saEmail string, spec servicemanager.DeploymentSpec) error {
	// This helper function's logic is now to create a service spec
	// that points directly to the spec.Image, without any BuildConfig.
	// The implementation would be very similar to the one in the previous step,
	// but the `desiredService` object would not have the `.BuildConfig` field.
	// ...
	return nil
}

func (d *CloudBuildDeployer) Teardown(ctx context.Context, serviceName string) error {
	// ...
	return nil
}

// conceptual placeholder
func (d *CloudBuildDeployer) uploadSourceToGCS(ctx context.Context, serviceName string) (string, error) {
	return fmt.Sprintf("source/%s-%d.tar.gz", serviceName, time.Now().Unix()), nil
}
