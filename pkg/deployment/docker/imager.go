// deployer/docker/builder.go
package docker

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"os/exec" // For docker commands
)

// CheckDockerAvailable checks if the Docker daemon is running and accessible.
func CheckDockerAvailable(ctx context.Context, logger zerolog.Logger) error {
	logger.Info().Msg("Checking Docker daemon availability...")
	cmd := exec.CommandContext(ctx, "docker", "info")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker not available: %w, output: %s", err, string(output))
	}
	logger.Info().Msg("Docker daemon is available.")
	return nil
}

// LoginToGCR attempts to log the Docker client into Google Container Registry (GCR)
// for the given project ID.
func LoginToGCR(ctx context.Context, projectID string, logger zerolog.Logger) error {
	registry := fmt.Sprintf("gcr.io/%s", projectID)
	logger.Info().Str("project_id", projectID).Msg("Attempting to login to GCR...")

	cmd := exec.CommandContext(ctx, "gcloud", "auth", "configure-docker", registry)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error().Err(err).Str("output", string(output)).Msg("Failed to configure Docker for GCR.")
		return fmt.Errorf("failed to configure Docker for GCR (%s): %w, output: %s", registry, err, string(output))
	}
	logger.Info().Msg("Successfully configured Docker for GCR.")
	return nil
}

// Imager provides methods for building and pushing Docker images.
type Imager struct {
	logger zerolog.Logger
}

// NewDockerImager creates a new Imager.
func NewDockerImager(logger zerolog.Logger) *Imager {
	return &Imager{
		logger: logger.With().Str("component", "Imager").Logger(),
	}
}

// BuildImage builds a Docker image
// dockerfilePath is the path to the Dockerfile to use (can be temporary).
// sourcePath is the build context directory (where source code is).
// imageTag is the full tag for the image (e.g., gcr.io/my-project/my-service:latest).
func (b *Imager) BuildImage(ctx context.Context, dockerfilePath, sourcePath, imageTag string) error {
	b.logger.Info().Str("image_tag", imageTag).Str("dockerfile_path", dockerfilePath).Str("context_path", sourcePath).Msg("Building Docker image...")

	// Docker build command: docker build -f <dockerfile-path> -t <image-tag> <context-path>
	cmd := exec.CommandContext(ctx, "docker", "build", "-f", dockerfilePath, "-t", imageTag, sourcePath)
	cmd.Stdout = b.logger // Direct output to logger
	cmd.Stderr = b.logger // Direct errors to logger

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to build Docker image %s: %w", imageTag, err)
	}

	b.logger.Info().Str("image_tag", imageTag).Msg("Docker image built successfully.")
	return nil
}

// PushImage pushes a Docker image to a registry.
func (b *Imager) PushImage(ctx context.Context, imageTag string) error {
	b.logger.Info().Str("image_tag", imageTag).Msg("Pushing Docker image...")

	cmd := exec.CommandContext(ctx, "docker", "push", imageTag)
	cmd.Stdout = b.logger
	cmd.Stderr = b.logger

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to push Docker image %s: %w", imageTag, err)
	}

	b.logger.Info().Str("image_tag", imageTag).Msg("Docker image pushed successfully.")
	return nil
}
