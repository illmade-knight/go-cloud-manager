package deployment

import (
	"archive/tar"
	"bytes"
	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"google.golang.org/api/googleapi"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"

	"cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/longrunning/autogen"
	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
)

// CloudBuildDeployer implements the ContainerDeployer interface using Google Cloud services.
type CloudBuildDeployer struct {
	projectID        string
	defaultRegion    string
	sourceBucket     string
	storageClient    *storage.Client
	cloudbuildClient *cloudbuild.Client
	runService       *run.Service
	lroClient        *longrunning.OperationsClient
	logger           zerolog.Logger
}

// NewCloudBuildDeployer creates a new, fully initialized deployer for production use.
func NewCloudBuildDeployer(ctx context.Context, projectID, defaultRegion, sourceBucket string, logger zerolog.Logger, opts ...option.ClientOption) (*CloudBuildDeployer, error) {

	storageClient, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage adminClient: %w", err)
	}
	buildClient, err := cloudbuild.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloud build adminClient: %w", err)
	}

	// Define the regional endpoint once.
	regionalEndpoint := fmt.Sprintf("%s-run.googleapis.com:443", defaultRegion)
	regionalOpts := append(opts, option.WithEndpoint(regionalEndpoint))

	// Create BOTH the run service and the LRO client with the same regional options.
	runService, err := run.NewService(ctx, regionalOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create run service: %w", err)
	}
	lroClient, err := longrunning.NewOperationsClient(ctx, regionalOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create longrunning adminClient: %w", err)
	}

	return &CloudBuildDeployer{
		projectID:        projectID,
		defaultRegion:    defaultRegion,
		sourceBucket:     sourceBucket,
		storageClient:    storageClient,
		cloudbuildClient: buildClient,
		runService:       runService,
		lroClient:        lroClient,
		logger:           logger.With().Str("component", "CloudBuildDeployer").Logger(),
	}, nil
}

// Deploy orchestrates the full upload, build, and deploy workflow. It returns the URL of the deployed service.
func (d *CloudBuildDeployer) Deploy(ctx context.Context, serviceName, serviceAccountEmail string, spec servicemanager.DeploymentSpec) (string, error) {
	d.logger.Info().Str("service", serviceName).Msg("Starting native cloud build and deploy workflow...")

	// ADDED: Ensure both GCS source bucket and Artifact Registry repo exist.
	if err := d.ensureSourceBucketExists(ctx); err != nil {
		return "", fmt.Errorf("failed to ensure GCS source bucket exists for service '%s': %w", serviceName, err)
	}
	if err := d.ensureArtifactRegistryRepoExists(ctx, spec); err != nil {
		return "", fmt.Errorf("failed to ensure artifact registry repo exists for service '%s': %w", serviceName, err)
	}

	sourceObject := fmt.Sprintf("source/%s-%d.tar.gz", serviceName, time.Now().UnixNano())
	if err := d.uploadSourceToGCS(ctx, spec.SourcePath, d.sourceBucket, sourceObject); err != nil {
		return "", fmt.Errorf("failed to upload source for service '%s': %w", serviceName, err)
	}
	d.logger.Info().Str("gcs_path", fmt.Sprintf("gs://%s/%s", d.sourceBucket, sourceObject)).Msg("Source code uploaded.")

	if spec.Image == "" {
		spec.Image = fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:%s", spec.Region, d.projectID, spec.ImageRepo, serviceName, uuid.New().String()[:8])
	}

	if err := d.triggerCloudBuild(ctx, sourceObject, serviceAccountEmail, spec); err != nil {
		return "", fmt.Errorf("cloud Build failed for service '%s': %w", serviceName, err)
	}
	d.logger.Info().Str("image", spec.Image).Msg("Cloud Build successful. Image is ready.")

	deployedSvc, err := d.createOrUpdateCloudRunService(ctx, serviceName, serviceAccountEmail, spec)
	if err != nil {
		return "", fmt.Errorf("cloud Run deployment failed for service '%s': %w", serviceName, err)
	}

	d.logger.Info().Str("service", serviceName).Str("url", deployedSvc.Uri).Msg("Service deployed successfully.")
	return deployedSvc.Uri, nil
}

// ADDED: New method to ensure the GCS source bucket exists.
func (d *CloudBuildDeployer) ensureSourceBucketExists(ctx context.Context) error {
	d.logger.Info().Str("bucket", d.sourceBucket).Msg("Verifying GCS source bucket...")
	bucket := d.storageClient.Bucket(d.sourceBucket)
	_, err := bucket.Attrs(ctx)
	if err == nil {
		d.logger.Info().Str("bucket", d.sourceBucket).Msg("GCS source bucket already exists.")
		return nil // Bucket exists, we are done.
	}
	if !errors.Is(err, storage.ErrBucketNotExist) {
		return fmt.Errorf("failed to check for GCS source bucket %s: %w", d.sourceBucket, err)
	}

	d.logger.Info().Str("bucket", d.sourceBucket).Msg("GCS source bucket not found, creating it now...")
	if err := bucket.Create(ctx, d.projectID, nil); err != nil {
		return fmt.Errorf("failed to create GCS source bucket %s: %w", d.sourceBucket, err)
	}

	d.logger.Info().Str("bucket", d.sourceBucket).Msg("✅ Successfully created GCS source bucket.")
	return nil
}

// ADDED: New method to ensure the Artifact Registry repository exists.
func (d *CloudBuildDeployer) ensureArtifactRegistryRepoExists(ctx context.Context, spec servicemanager.DeploymentSpec) error {
	repoName := spec.ImageRepo
	region := d.defaultRegion // Use the deployer's default region
	if spec.Region != "" {
		region = spec.Region // But prefer the service-specific region if provided
	}

	if repoName == "" {
		return errors.New("cannot ensure repository exists: ImageRepo is not defined in the deployment spec")
	}

	d.logger.Info().Str("repository", repoName).Str("region", region).Msg("Verifying Artifact Registry repository...")

	arClient, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create artifact registry client: %w", err)
	}
	defer arClient.Close()

	parent := fmt.Sprintf("projects/%s/locations/%s", d.projectID, region)
	fullRepoName := fmt.Sprintf("%s/repositories/%s", parent, repoName)

	_, err = arClient.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{Name: fullRepoName})
	if err == nil {
		d.logger.Info().Str("repository", fullRepoName).Msg("Artifact Registry repository already exists.")
		return nil
	}

	if status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to check for repository '%s': %w", fullRepoName, err)
	}

	d.logger.Info().Str("repository", fullRepoName).Msg("Repository not found, creating it now...")
	createOp, err := arClient.CreateRepository(ctx, &artifactregistrypb.CreateRepositoryRequest{
		Parent:       parent,
		RepositoryId: repoName,
		Repository:   &artifactregistrypb.Repository{Format: artifactregistrypb.Repository_DOCKER},
	})
	if err != nil {
		return fmt.Errorf("failed to start repository creation for '%s': %w", repoName, err)
	}

	_, err = createOp.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for repository creation '%s': %w", repoName, err)
	}

	d.logger.Info().Str("repository", fullRepoName).Msg("✅ Successfully created Artifact Registry repository.")
	return nil
}

// Teardown deletes the specified Cloud Run service.
func (d *CloudBuildDeployer) Teardown(ctx context.Context, serviceName string) error {
	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", d.projectID, d.defaultRegion, serviceName)
	d.logger.Info().Str("service", serviceName).Msg("Tearing down Cloud Run service...")

	op, err := d.runService.Projects.Locations.Services.Delete(fullServiceName).Context(ctx).Do()
	if err != nil {
		if e, ok := status.FromError(err); ok && e.Code() == codes.NotFound {
			d.logger.Info().Str("service", serviceName).Msg("Service already deleted.")
			return nil
		}
		return fmt.Errorf("failed to trigger Cloud Run delete operation for '%s': %w", serviceName, err)
	}
	if err = d.pollRunOperation(ctx, op.Name); err != nil {
		return err
	}
	d.logger.Info().Str("service", serviceName).Msg("Service torn down successfully.")
	return nil
}

// --- Private Helper Methods ---
func (d *CloudBuildDeployer) createOrUpdateCloudRunService(ctx context.Context, serviceName, saEmail string, spec servicemanager.DeploymentSpec) (*run.GoogleCloudRunV2Service, error) {
	parent := fmt.Sprintf("projects/%s/locations/%s", d.projectID, d.defaultRegion)
	fullServiceName := fmt.Sprintf("%s/services/%s", parent, serviceName)
	desiredService := buildRunServiceConfig(saEmail, spec)

	existingSvc, err := d.runService.Projects.Locations.Services.Get(fullServiceName).Context(ctx).Do()
	var op *run.GoogleLongrunningOperation

	if err != nil {
		var gErr *googleapi.Error
		if errors.As(err, &gErr) && gErr.Code == http.StatusNotFound {
			d.logger.Info().Str("service", serviceName).Msg("Service does not exist, creating it now.")
			op, err = d.runService.Projects.Locations.Services.Create(parent, desiredService).ServiceId(serviceName).Context(ctx).Do()
		} else {
			return nil, fmt.Errorf("failed to get status of existing Cloud Run service: %w", err)
		}
	} else {
		// Service exists, so patch it.
		d.logger.Info().Str("service", serviceName).Msg("Service already exists, updating it now.")
		desiredService.Etag = existingSvc.Etag
		op, err = d.runService.Projects.Locations.Services.Patch(fullServiceName, desiredService).Context(ctx).Do()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to trigger Cloud Run create/update operation: %w", err)
	}
	if err = d.pollRunOperation(ctx, op.Name); err != nil {
		return nil, err
	}
	return d.runService.Projects.Locations.Services.Get(fullServiceName).Context(ctx).Do()
}

func (d *CloudBuildDeployer) pollRunOperation(ctx context.Context, opName string) error {
	for {
		getOp, err := d.lroClient.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: opName})
		if err != nil {
			return fmt.Errorf("failed to poll Cloud Run operation status: %w", err)
		}
		if getOp.Done {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
}

func buildRunServiceConfig(saEmail string, spec servicemanager.DeploymentSpec) *run.GoogleCloudRunV2Service {
	var envVars []*run.GoogleCloudRunV2EnvVar
	for k, v := range spec.EnvironmentVars {
		envVars = append(envVars, &run.GoogleCloudRunV2EnvVar{Name: k, Value: v})
	}

	return &run.GoogleCloudRunV2Service{
		Template: &run.GoogleCloudRunV2RevisionTemplate{
			ServiceAccount: saEmail,
			Containers: []*run.GoogleCloudRunV2Container{
				{
					Image: spec.Image,
					Env:   envVars,
					Resources: &run.GoogleCloudRunV2ResourceRequirements{
						Limits: map[string]string{"cpu": spec.CPU, "memory": spec.Memory},
					},
				},
			},
			Scaling: &run.GoogleCloudRunV2RevisionScaling{
				MinInstanceCount: spec.MinInstances,
				MaxInstanceCount: spec.MaxInstances,
			},
		},
	}
}

func (d *CloudBuildDeployer) uploadSourceToGCS(ctx context.Context, sourceDir, bucket, objectName string) error {
	buf := new(bytes.Buffer)
	gzipWriter := gzip.NewWriter(buf)
	tarWriter := tar.NewWriter(gzipWriter)

	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		// Get the relative path of the file.
		header.Name, err = filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		// This is the crucial fix: ensure all path separators are forward
		// slashes for compatibility with the Linux build environment.
		header.Name = filepath.ToSlash(header.Name)

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := io.Copy(tarWriter, file); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk source directory '%s': %w", sourceDir, err)
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("failed to close tar writer: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("failed to close gzip writer: %w", err)
	}
	w := d.storageClient.Bucket(bucket).Object(objectName).NewWriter(ctx)
	if _, err = io.Copy(w, buf); err != nil {
		w.Close()
		return fmt.Errorf("failed to copy source to GCS: %w", err)
	}
	return w.Close()
}
func (d *CloudBuildDeployer) triggerCloudBuild(ctx context.Context, gcsSourceObject, serviceAccountEmail string, spec servicemanager.DeploymentSpec) error {
	d.logger.Info().Str("service account", serviceAccountEmail).Msg("Triggering Cloud Build")

	mainBuildCommand := fmt.Sprintf("pack build %s --path %s", spec.Image, spec.BuildableModulePath)

	var buildSteps = []*cloudbuildpb.BuildStep{
		{
			Name:       "gcr.io/k8s-skaffold/pack",
			Id:         "pre-buildpack",
			Entrypoint: "sh",
			Args:       []string{"-c", "chmod a+w /workspace && pack config default-builder gcr.io/buildpacks/builder:latest"},
		},
		{
			Name:       "gcr.io/k8s-skaffold/pack",
			Id:         "build",
			Entrypoint: "sh",
			Args:       []string{"-c", mainBuildCommand},
		},
	}

	// If we are building a module in a subdirectory, prepend the copy step.
	if spec.BuildableModulePath != "" && spec.BuildableModulePath != "." {
		d.logger.Info().Str("module_path", spec.BuildableModulePath).Msg("Prepending 'copy-module-files' step for monorepo build")
		copyStep := &cloudbuildpb.BuildStep{
			Name:       "gcr.io/cloud-builders/gcloud",
			Id:         "copy-module-files",
			Entrypoint: "bash",
			Args: []string{
				"-c",
				// This robust command copies go.sum only if it exists.
				fmt.Sprintf("cp go.mod %s/ && ([ -f go.sum ] && cp go.sum %s/ || true)", spec.BuildableModulePath, spec.BuildableModulePath),
			},
		}
		// Prepend the copy step to the beginning of the slice.
		buildSteps = append([]*cloudbuildpb.BuildStep{copyStep}, buildSteps...)
	}

	//fullSaName := fmt.Sprintf("projects/%s/serviceAccounts/%s", d.projectID, serviceAccountEmail)

	// Now, construct the final build request with the correct steps.
	req := &cloudbuildpb.CreateBuildRequest{
		ProjectId: d.projectID,
		Build: &cloudbuildpb.Build{
			// It's easier if we never fill in ServiceAccount field, that way it chooses the right one for us
			// ServiceAccount: the cloud build service account,
			Source: &cloudbuildpb.Source{
				Source: &cloudbuildpb.Source_StorageSource{
					StorageSource: &cloudbuildpb.StorageSource{
						Bucket: d.sourceBucket,
						Object: gcsSourceObject,
					},
				},
			},
			Steps:  buildSteps,
			Images: []string{spec.Image},
			Options: &cloudbuildpb.BuildOptions{
				Logging: cloudbuildpb.BuildOptions_CLOUD_LOGGING_ONLY,
			},
		},
	}

	op, err := d.cloudbuildClient.CreateBuild(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create cloud build: %w", err)
	}

	meta, err := op.Metadata()
	if err != nil {
		d.logger.Warn().Err(err).Msg("Could not get initial build metadata")
	} else {
		d.logger.Info().Str("build_id", meta.GetBuild().GetId()).Msg("Waiting for Cloud Build job to complete...")
	}

	resp, err := op.Wait(ctx)
	if err != nil {
		return fmt.Errorf("cloud build operation failed during wait: %w", err)
	}

	if resp.GetStatus() != cloudbuildpb.Build_SUCCESS {
		return fmt.Errorf("cloud build failed with final status: %s", resp.GetStatus())
	}

	return nil
}
