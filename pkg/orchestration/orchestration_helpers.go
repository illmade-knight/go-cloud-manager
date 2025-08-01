package orchestration

import (
	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"cloud.google.com/go/pubsub"
	"context"
	"fmt"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"time"
)

// WaitForState is a helper function that blocks until the Orchestrator's state
// channel emits a specific target state. It includes a timeout to prevent indefinite waiting.
func WaitForState(ctx context.Context, orch *Orchestrator, targetState OrchestratorState) error {
	for {
		select {
		case state, ok := <-orch.StateChan():
			if !ok {
				return fmt.Errorf("orchestrator state channel closed prematurely while waiting for state: %s", targetState)
			}
			log.Info().Str("state", string(state)).Msg("Orchestrator state changed")
			if state == targetState {
				return nil // Success!
			}
			if state == StateError {
				return fmt.Errorf("orchestrator entered an error state while waiting for state: %s", targetState)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
			return fmt.Errorf("timed out waiting for orchestrator state: %s", targetState)
		}
	}
}

// verifyPrerequisites is the main entry point for pre-flight checks before a deployment.
func verifyPrerequisites(ctx context.Context, arch *servicemanager.MicroserviceArchitecture) error {
	arClient, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create artifactregistry client for validation: %w", err)
	}
	defer func() {
		_ = arClient.Close()
	}()

	err = verifyRegion(ctx, arClient, arch)
	if err != nil {
		return err
	}

	// Assuming all services in the architecture use the same image repo for simplicity.
	repoName := arch.ServiceManagerSpec.Deployment.ImageRepo
	if repoName != "" {
		err = ensureArtifactRegistryRepositoryExists(ctx, arClient, arch.ProjectID, arch.Region, repoName)
		if err != nil {
			return err
		}
	}
	return nil
}

// verifyRegion checks that the region specified in the architecture is a valid GCP location.
// It performs this check by making a lightweight API call to a regional endpoint.
func verifyRegion(ctx context.Context, client *artifactregistry.Client, arch *servicemanager.MicroserviceArchitecture) error {
	log.Info().Str("region", arch.Region).Msg("Verifying GCP region...")

	// We test the region's validity by trying to get a non-existent repository.
	// This is a lightweight and reliable way to check if the regional endpoint is valid.
	repoPath := fmt.Sprintf("projects/%s/locations/%s/repositories/region-validation-check", arch.ProjectID, arch.Region)
	_, err := client.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{Name: repoPath})

	// If the error is anything other than "Not Found", it could indicate an invalid region.
	if err != nil && status.Code(err) != codes.NotFound {
		if strings.Contains(err.Error(), "Location not found") {
			return fmt.Errorf("invalid region specified in architecture: '%s' is not a valid location", arch.Region)
		}
		// Handle other potential errors like permission denied.
		return fmt.Errorf("failed to validate region '%s': %w", arch.Region, err)
	}

	log.Info().Str("region", arch.Region).Msg("Region is valid.")
	return nil
}

// ensureArtifactRegistryRepositoryExists creates the repository if it's missing.
func ensureArtifactRegistryRepositoryExists(ctx context.Context, client *artifactregistry.Client, projectID, location, repoName string) error {
	repoPath := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", projectID, location, repoName)
	_, err := client.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{Name: repoPath})
	if err == nil {
		log.Info().Str("repository", repoName).Msg("Artifact Registry repository already exists.")
		return nil
	}

	if status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to check for repository '%s': %w", repoName, err)
	}

	log.Info().Str("repository", repoName).Str("location", location).Msg("Artifact Registry repository not found, creating it now...")
	parent := fmt.Sprintf("projects/%s/locations/%s", projectID, location)
	op, err := client.CreateRepository(ctx, &artifactregistrypb.CreateRepositoryRequest{
		Parent:       parent,
		RepositoryId: repoName,
		Repository: &artifactregistrypb.Repository{
			Format: artifactregistrypb.Repository_DOCKER,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to trigger repository creation for '%s': %w", repoName, err)
	}

	_, err = op.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for repository creation for '%s': %w", repoName, err)
	}
	log.Info().Str("repository", repoName).Msg("Successfully created repository.")
	return nil
}

// ensureTopicExists is a private helper to create a Pub/Sub topic if it doesn't already exist.
func ensureTopicExists(ctx context.Context, client *pubsub.Client, topicID string) (*pubsub.Topic, error) {
	topic := client.Topic(topicID)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check for topic %s: %w", topicID, err)
	}
	if !exists {
		log.Info().Str("topic", topicID).Msg("Topic not found, creating it now.")
		topic, err = client.CreateTopic(ctx, topicID)
		if err != nil {
			return nil, fmt.Errorf("failed to create topic %s: %w", topicID, err)
		}
	}
	return topic, nil
}
