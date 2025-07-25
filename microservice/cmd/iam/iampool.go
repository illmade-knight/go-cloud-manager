package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"

	"cloud.google.com/go/iam/admin/apiv1"
	"cloud.google.com/go/iam/admin/apiv1/adminpb"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	// Setup command-line flags for configuration
	projectID := flag.String("project-id", "", "GCP Project ID (required)")
	prefix := flag.String("prefix", "", "Prefix for the service account names (e.g., 'it-iam-') (required)")
	size := flag.Int("size", 0, "Number of service accounts to create in the pool (required)")
	flag.Parse()

	// Validate inputs
	if *projectID == "" || *prefix == "" || *size <= 0 {
		log.Fatal().Msg("All flags (-project-id, -prefix, -size) are required and size must be > 0.")
		flag.Usage()
		return
	}

	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	ctx := context.Background()

	// Create the necessary GCP client.
	iamAdminClient, err := admin.NewIamClient(ctx)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create IAM admin client")
	}
	defer iamAdminClient.Close()

	// Run the pool creation logic.
	if err := createPool(ctx, iamAdminClient, *projectID, *prefix, *size, logger); err != nil {
		logger.Fatal().Err(err).Msg("Failed to create service account pool")
	}
}

// createPool manages the concurrent creation of service accounts.
func createPool(ctx context.Context, client *admin.IamClient, projectID, prefix string, size int, logger zerolog.Logger) error {
	logger.Info().Str("prefix", prefix).Int("size", size).Msg("Ensuring test service account pool exists...")

	var wg sync.WaitGroup
	errChan := make(chan error, size)

	for i := 0; i < size; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			// Generate a consistent name for each account in the pool
			accountID := fmt.Sprintf("%s%d", prefix, index)
			email, err := ensureServiceAccountExists(ctx, client, projectID, accountID)
			if err != nil {
				errChan <- fmt.Errorf("failed to create pool SA #%d (%s): %w", index, accountID, err)
				return
			}
			logger.Info().Str("email", email).Msg("Service account in pool is ready.")
		}(i)
	}

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("encountered %d errors while creating SA pool: %v", len(allErrors), allErrors)
	}

	logger.Info().Msg("✅ Test service account pool is ready.")
	return nil
}

// ensureServiceAccountExists creates a service account if it does not already exist.
func ensureServiceAccountExists(ctx context.Context, client *admin.IamClient, projectID, accountID string) (string, error) {
	email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, projectID)
	resourceName := fmt.Sprintf("projects/%s/serviceAccounts/%s", projectID, email)

	// Check if the service account already exists.
	_, err := client.GetServiceAccount(ctx, &adminpb.GetServiceAccountRequest{Name: resourceName})
	if err == nil {
		// SA already exists, nothing to do.
		return email, nil
	}
	if status.Code(err) != codes.NotFound {
		return "", fmt.Errorf("failed to check for service account %s: %w", email, err)
	}

	// If it was not found, create it.
	createReq := &adminpb.CreateServiceAccountRequest{
		Name:      "projects/" + projectID,
		AccountId: accountID,
		ServiceAccount: &adminpb.ServiceAccount{
			DisplayName: "Pool SA for " + accountID,
		},
	}
	sa, err := client.CreateServiceAccount(ctx, createReq)
	if err != nil {
		return "", fmt.Errorf("failed to create service account %s: %w", email, err)
	}
	return sa.Email, nil
}
