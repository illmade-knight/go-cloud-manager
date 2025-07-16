package deployment

import (
	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"context"
	"fmt"

	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnsureArtifactRegistryRepositoryExists checks if a repository exists and creates it if not.
func EnsureArtifactRegistryRepositoryExists(
	ctx context.Context,
	projectID, location, repositoryID string,
	logger zerolog.Logger,
	opts ...option.ClientOption,
) error {
	logger.Info().Str("repository", repositoryID).Msg("Checking for Artifact Registry repository...")

	client, err := artifactregistry.NewClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create artifact registry adminClient: %w", err)
	}
	defer client.Close()

	parent := fmt.Sprintf("projects/%s/locations/%s", projectID, location)
	repoName := fmt.Sprintf("%s/repositories/%s", parent, repositoryID)

	// Check if the repository already exists.
	_, err = client.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{Name: repoName})
	if err == nil {
		logger.Info().Str("repository", repositoryID).Msg("Artifact Registry repository already exists.")
		return nil // Repository exists, nothing to do.
	}

	// If the error is anything other than "Not Found", it's a real problem.
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		return fmt.Errorf("failed to check for repository '%s': %w", repositoryID, err)
	}

	// Repository does not exist, so create it.
	logger.Info().Str("repository", repositoryID).Msg("Repository not found, creating it now...")
	createReq := &artifactregistrypb.CreateRepositoryRequest{
		Parent:       parent,
		RepositoryId: repositoryID,
		Repository: &artifactregistrypb.Repository{
			Format:      artifactregistrypb.Repository_DOCKER,
			Description: "Repository created automatically by deployment manager",
		},
	}

	op, err := client.CreateRepository(ctx, createReq)
	if err != nil {
		return fmt.Errorf("failed to trigger repository creation: %w", err)
	}

	// Wait for the creation operation to complete.
	if _, err := op.Wait(ctx); err != nil {
		return fmt.Errorf("failed to create repository '%s': %w", repositoryID, err)
	}

	logger.Info().Str("repository", repositoryID).Msg("Successfully created Artifact Registry repository.")
	return nil
}
