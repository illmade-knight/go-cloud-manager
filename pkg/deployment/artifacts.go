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

// EnsureArtifactRegistryRepositoryExists checks if a specific Artifact Registry repository
// exists in the given project and location. If it does not exist, it creates it.
// This function is idempotent and safe to call multiple times.
func EnsureArtifactRegistryRepositoryExists(
	ctx context.Context,
	projectID, location, repositoryID string,
	logger zerolog.Logger,
	opts ...option.ClientOption,
) error {
	log := logger.With().Str("repository", repositoryID).Str("location", location).Logger()
	log.Info().Msg("Checking for Artifact Registry repository...")

	client, err := artifactregistry.NewClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create artifact registry client: %w", err)
	}
	defer func() {
		_ = client.Close()
	}()

	parent := fmt.Sprintf("projects/%s/locations/%s", projectID, location)
	repoName := fmt.Sprintf("%s/repositories/%s", parent, repositoryID)

	// Check if the repository already exists.
	_, err = client.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{Name: repoName})
	if err == nil {
		log.Info().Msg("Artifact Registry repository already exists.")
		return nil // Repository exists, nothing more to do.
	}

	// If the error is anything other than "Not Found", it's an unexpected issue.
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		return fmt.Errorf("failed to check for repository '%s': %w", repositoryID, err)
	}

	// Repository does not exist, so create it.
	log.Info().Msg("Repository not found, creating it now...")
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

	// Wait for the long-running creation operation to complete.
	_, err = op.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to create repository '%s': %w", repositoryID, err)
	}

	log.Info().Msg("✅ Successfully created Artifact Registry repository.")
	return nil
}
