package deployment

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/longrunning/autogen"
	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- Interfaces for Dependencies ---
// REFACTOR_NOTE: By depending on these interfaces instead of concrete clients,
// the CloudBuildDeployer becomes easy to unit test with mocks.

// storageAPI defines the contract for storage operations needed by the deployer.
type storageAPI interface {
	EnsureBucketExists(ctx context.Context, bucketName string) error
	Upload(ctx context.Context, sourceDir, bucket, objectName string) error
}

// artifactRegistryAPI defines the contract for Artifact Registry operations.
type artifactRegistryAPI interface {
	EnsureRepositoryExists(ctx context.Context, repoName, region string) error
}

// cloudBuildAPI defines the contract for Cloud Build operations.
type cloudBuildAPI interface {
	TriggerBuildAndWait(ctx context.Context, gcsSourceObject string, spec servicemanager.DeploymentSpec) error
}

// cloudRunAPI defines the contract for Cloud Run operations.
type cloudRunAPI interface {
	CreateOrUpdate(ctx context.Context, serviceName, saEmail string, spec servicemanager.DeploymentSpec) (*run.GoogleCloudRunV2Service, error)
	Delete(ctx context.Context, serviceName string) error
}

// --- CloudBuildDeployer ---

// CloudBuildDeployer implements the ContainerDeployer interface using Google Cloud services.
type CloudBuildDeployer struct {
	projectID        string
	defaultRegion    string
	sourceBucket     string
	logger           zerolog.Logger
	storage          storageAPI
	artifactRegistry artifactRegistryAPI
	builder          cloudBuildAPI
	runner           cloudRunAPI
}

// NewCloudBuildDeployer creates a new, fully initialized deployer for production use.
// It constructs the concrete adapters that wrap the real Google Cloud clients.
func NewCloudBuildDeployer(ctx context.Context, projectID, defaultRegion, sourceBucket string, logger zerolog.Logger, opts ...option.ClientOption) (*CloudBuildDeployer, error) {
	storageClient, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage client: %w", err)
	}
	storageAdapter := &googleStorageAdapter{
		client:    storageClient,
		projectID: projectID,
		logger:    logger,
	}

	arAdapter, err := newGoogleArtifactRegistryAdapter(ctx, projectID, logger, opts...)
	if err != nil {
		return nil, err
	}

	buildClient, err := cloudbuild.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloud build client: %w", err)
	}
	builderAdapter := &googleCloudBuildAdapter{
		client:    buildClient,
		projectID: projectID,
		bucket:    sourceBucket,
		logger:    logger,
	}

	runAdapter, err := newGoogleCloudRunAdapter(ctx, projectID, defaultRegion, logger, opts...)
	if err != nil {
		return nil, err
	}

	return &CloudBuildDeployer{
		projectID:        projectID,
		defaultRegion:    defaultRegion,
		sourceBucket:     sourceBucket,
		logger:           logger.With().Str("component", "CloudBuildDeployer").Logger(),
		storage:          storageAdapter,
		artifactRegistry: arAdapter,
		builder:          builderAdapter,
		runner:           runAdapter,
	}, nil
}

// NewCloudBuildDeployerForTest creates a deployer with mocked dependencies for unit testing.
func NewCloudBuildDeployerForTest(projectID, region, bucket string, logger zerolog.Logger, storage storageAPI, ar artifactRegistryAPI, builder cloudBuildAPI, runner cloudRunAPI) *CloudBuildDeployer {
	return &CloudBuildDeployer{
		projectID:        projectID,
		defaultRegion:    region,
		sourceBucket:     bucket,
		logger:           logger,
		storage:          storage,
		artifactRegistry: ar,
		builder:          builder,
		runner:           runner,
	}
}

// Deploy orchestrates the full upload, build, and deploy workflow.
func (d *CloudBuildDeployer) Deploy(ctx context.Context, serviceName, serviceAccountEmail string, spec servicemanager.DeploymentSpec) (string, error) {
	d.logger.Info().Str("service", serviceName).Msg("Starting native cloud build and deploy workflow...")

	// 1. Ensure prerequisite infrastructure exists.
	err := d.storage.EnsureBucketExists(ctx, d.sourceBucket)
	if err != nil {
		return "", fmt.Errorf("failed to ensure GCS source bucket exists for service '%s': %w", serviceName, err)
	}
	err = d.artifactRegistry.EnsureRepositoryExists(ctx, spec.ImageRepo, spec.Region)
	if err != nil {
		return "", fmt.Errorf("failed to ensure artifact registry repo exists for service '%s': %w", serviceName, err)
	}

	// 2. Archive and upload source code.
	sourceObject := fmt.Sprintf("source/%s-%d.tar.gz", serviceName, time.Now().UnixNano())
	err = d.storage.Upload(ctx, spec.SourcePath, d.sourceBucket, sourceObject)
	if err != nil {
		return "", fmt.Errorf("failed to upload source for service '%s': %w", serviceName, err)
	}
	d.logger.Info().Str("gcs_path", fmt.Sprintf("gs://%s/%s", d.sourceBucket, sourceObject)).Msg("Source code uploaded.")

	// 3. Trigger Cloud Build to create the container image.
	if spec.Image == "" {
		spec.Image = fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:%s", spec.Region, d.projectID, spec.ImageRepo, serviceName, uuid.New().String()[:8])
	}
	err = d.builder.TriggerBuildAndWait(ctx, sourceObject, spec)
	if err != nil {
		return "", fmt.Errorf("cloud Build failed for service '%s': %w", serviceName, err)
	}
	d.logger.Info().Str("image", spec.Image).Msg("Cloud Build successful. Image is ready.")

	// 4. Deploy the newly built image to Cloud Run.
	deployedSvc, err := d.runner.CreateOrUpdate(ctx, serviceName, serviceAccountEmail, spec)
	if err != nil {
		return "", fmt.Errorf("cloud Run deployment failed for service '%s': %w", serviceName, err)
	}

	d.logger.Info().Str("service", serviceName).Str("url", deployedSvc.Uri).Msg("Service deployed successfully.")
	return deployedSvc.Uri, nil
}

// Teardown deletes the specified Cloud Run service.
func (d *CloudBuildDeployer) Teardown(ctx context.Context, serviceName string) error {
	d.logger.Info().Str("service", serviceName).Msg("Tearing down Cloud Run service...")
	err := d.runner.Delete(ctx, serviceName)
	if err != nil {
		return fmt.Errorf("failed to tear down service '%s': %w", serviceName, err)
	}
	d.logger.Info().Str("service", serviceName).Msg("Service torn down successfully.")
	return nil
}

// --- Concrete Adapters for Google Cloud ---

// googleStorageAdapter implements the storageAPI interface.
type googleStorageAdapter struct {
	client    *storage.Client
	projectID string
	logger    zerolog.Logger
}

func (a *googleStorageAdapter) EnsureBucketExists(ctx context.Context, bucketName string) error {
	log := a.logger.With().Str("bucket", bucketName).Logger()
	log.Info().Msg("Verifying GCS source bucket...")
	bucket := a.client.Bucket(bucketName)
	_, err := bucket.Attrs(ctx)
	if err == nil {
		log.Info().Msg("GCS source bucket already exists.")
		return nil
	}
	if !errors.Is(err, storage.ErrBucketNotExist) {
		return fmt.Errorf("failed to check for GCS source bucket %s: %w", bucketName, err)
	}

	log.Info().Msg("GCS source bucket not found, creating it now...")
	err = bucket.Create(ctx, a.projectID, nil)
	if err != nil {
		return fmt.Errorf("failed to create GCS source bucket %s: %w", bucketName, err)
	}
	log.Info().Msg("✅ Successfully created GCS source bucket.")
	return nil
}

func (a *googleStorageAdapter) Upload(ctx context.Context, sourceDir, bucket, objectName string) error {
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
		header.Name, err = filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(header.Name)

		err = tarWriter.WriteHeader(header)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func(file *os.File) {
			_ = file.Close()
		}(file)
		_, err = io.Copy(tarWriter, file)
		return err
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

	w := a.client.Bucket(bucket).Object(objectName).NewWriter(ctx)
	if _, err = io.Copy(w, buf); err != nil {
		_ = w.Close()
		return fmt.Errorf("failed to copy source to GCS: %w", err)
	}
	return w.Close()
}

// googleArtifactRegistryAdapter implements the artifactRegistryAPI interface.
type googleArtifactRegistryAdapter struct {
	client    *artifactregistry.Client
	projectID string
	logger    zerolog.Logger
}

func newGoogleArtifactRegistryAdapter(ctx context.Context, projectID string, logger zerolog.Logger, opts ...option.ClientOption) (*googleArtifactRegistryAdapter, error) {
	client, err := artifactregistry.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create artifact registry client: %w", err)
	}
	return &googleArtifactRegistryAdapter{
		client:    client,
		projectID: projectID,
		logger:    logger,
	}, nil
}

func (a *googleArtifactRegistryAdapter) EnsureRepositoryExists(ctx context.Context, repoName, region string) error {
	if repoName == "" {
		return errors.New("repository name is required")
	}
	log := a.logger.With().Str("repository", repoName).Str("region", region).Logger()
	log.Info().Msg("Verifying Artifact Registry repository...")

	parent := fmt.Sprintf("projects/%s/locations/%s", a.projectID, region)
	fullRepoName := fmt.Sprintf("%s/repositories/%s", parent, repoName)

	_, err := a.client.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{Name: fullRepoName})
	if err == nil {
		log.Info().Msg("Artifact Registry repository already exists.")
		return nil
	}
	if status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to check for repository '%s': %w", fullRepoName, err)
	}

	log.Info().Msg("Repository not found, creating it now...")
	createOp, err := a.client.CreateRepository(ctx, &artifactregistrypb.CreateRepositoryRequest{
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
	log.Info().Msg("✅ Successfully created Artifact Registry repository.")
	return nil
}

// googleCloudBuildAdapter implements the cloudBuildAPI interface.
type googleCloudBuildAdapter struct {
	client    *cloudbuild.Client
	projectID string
	bucket    string
	logger    zerolog.Logger
}

func (a *googleCloudBuildAdapter) TriggerBuildAndWait(ctx context.Context, gcsSourceObject string, spec servicemanager.DeploymentSpec) error {
	a.logger.Info().Str("image", spec.Image).Msg("Triggering Cloud Build")

	mainBuildCommand := fmt.Sprintf("pack build %s --path %s", spec.Image, spec.BuildableModulePath)
	buildSteps := []*cloudbuildpb.BuildStep{
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

	req := &cloudbuildpb.CreateBuildRequest{
		ProjectId: a.projectID,
		Build: &cloudbuildpb.Build{
			Source: &cloudbuildpb.Source{
				Source: &cloudbuildpb.Source_StorageSource{
					StorageSource: &cloudbuildpb.StorageSource{
						Bucket: a.bucket,
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

	op, err := a.client.CreateBuild(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create cloud build: %w", err)
	}
	meta, err := op.Metadata()
	if err != nil {
		a.logger.Warn().Err(err).Msg("Could not get initial build metadata")
	} else {
		a.logger.Info().Str("build_id", meta.GetBuild().GetId()).Msg("Waiting for Cloud Build job to complete...")
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

// googleCloudRunAdapter implements the cloudRunAPI interface.
type googleCloudRunAdapter struct {
	projectID     string
	defaultRegion string
	runService    *run.Service
	lroClient     *longrunning.OperationsClient
	logger        zerolog.Logger
}

func newGoogleCloudRunAdapter(ctx context.Context, projectID, defaultRegion string, logger zerolog.Logger, opts ...option.ClientOption) (*googleCloudRunAdapter, error) {
	regionalEndpoint := fmt.Sprintf("%s-run.googleapis.com:443", defaultRegion)
	regionalOpts := append(opts, option.WithEndpoint(regionalEndpoint))
	runService, err := run.NewService(ctx, regionalOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create run service: %w", err)
	}
	lroClient, err := longrunning.NewOperationsClient(ctx, regionalOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create longrunning client: %w", err)
	}
	return &googleCloudRunAdapter{
		projectID:     projectID,
		defaultRegion: defaultRegion,
		runService:    runService,
		lroClient:     lroClient,
		logger:        logger,
	}, nil
}

func (a *googleCloudRunAdapter) CreateOrUpdate(ctx context.Context, serviceName, saEmail string, spec servicemanager.DeploymentSpec) (*run.GoogleCloudRunV2Service, error) {
	parent := fmt.Sprintf("projects/%s/locations/%s", a.projectID, a.defaultRegion)
	fullServiceName := fmt.Sprintf("%s/services/%s", parent, serviceName)
	desiredService := buildRunServiceConfig(saEmail, spec)

	existingSvc, err := a.runService.Projects.Locations.Services.Get(fullServiceName).Context(ctx).Do()
	var op *run.GoogleLongrunningOperation

	if err != nil {
		var gErr *googleapi.Error
		if errors.As(err, &gErr) && gErr.Code == http.StatusNotFound {
			a.logger.Info().Str("service", serviceName).Msg("Service does not exist, creating it now.")
			op, err = a.runService.Projects.Locations.Services.Create(parent, desiredService).ServiceId(serviceName).Context(ctx).Do()
		} else {
			return nil, fmt.Errorf("failed to get status of existing Cloud Run service: %w", err)
		}
	} else {
		a.logger.Info().Str("service", serviceName).Msg("Service already exists, updating it now.")
		desiredService.Etag = existingSvc.Etag
		op, err = a.runService.Projects.Locations.Services.Patch(fullServiceName, desiredService).Context(ctx).Do()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to trigger Cloud Run create/update operation: %w", err)
	}
	err = a.pollRunOperation(ctx, op.Name)
	if err != nil {
		return nil, err
	}
	return a.runService.Projects.Locations.Services.Get(fullServiceName).Context(ctx).Do()
}

func (a *googleCloudRunAdapter) Delete(ctx context.Context, serviceName string) error {
	fullServiceName := fmt.Sprintf("projects/%s/locations/%s/services/%s", a.projectID, a.defaultRegion, serviceName)
	op, err := a.runService.Projects.Locations.Services.Delete(fullServiceName).Context(ctx).Do()
	if err != nil {
		if e, ok := status.FromError(err); ok && e.Code() == codes.NotFound {
			return nil // Already deleted.
		}
		return fmt.Errorf("failed to trigger Cloud Run delete operation for '%s': %w", serviceName, err)
	}
	return a.pollRunOperation(ctx, op.Name)
}

func (a *googleCloudRunAdapter) pollRunOperation(ctx context.Context, opName string) error {
	for {
		getOp, err := a.lroClient.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: opName})
		if err != nil {
			return fmt.Errorf("failed to poll Cloud Run operation status: %w", err)
		}
		if getOp.Done {
			return nil
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// buildRunServiceConfig constructs the configuration for a Cloud Run service from a DeploymentSpec.
// It correctly processes both plain-text and secret-backed environment variables.
func buildRunServiceConfig(saEmail string, spec servicemanager.DeploymentSpec) *run.GoogleCloudRunV2Service {
	var envVars []*run.GoogleCloudRunV2EnvVar

	// Process plain-text environment variables.
	for k, v := range spec.EnvironmentVars {
		envVars = append(envVars, &run.GoogleCloudRunV2EnvVar{Name: k, Value: v})
	}

	// Process secret-backed environment variables.
	for _, secretVar := range spec.SecretEnvironmentVars {
		envVars = append(envVars, &run.GoogleCloudRunV2EnvVar{
			Name: secretVar.Name,
			ValueSource: &run.GoogleCloudRunV2EnvVarSource{
				SecretKeyRef: &run.GoogleCloudRunV2SecretKeySelector{
					Secret:  secretVar.ValueFrom,
					Version: "latest",
				},
			},
		})
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
