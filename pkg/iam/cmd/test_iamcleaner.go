// Filename: cleanup_sa_roles.go
//
// This is a standalone utility to clean all IAM roles from service accounts
// in a project that match a specific prefix.
//
// How to run:
// go run cleanup_sa_roles.go -projectID="your-gcp-project-id" -prefix="it-"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	giam "cloud.google.com/go/iam"
	"cloud.google.com/go/iam/apiv1/iampb"
	// NOTE: You may need to adjust this import path to match your project's module path.
	// This path is assumed based on the files you've provided.
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// --- Command-line flags ---
	projectID := flag.String("projectID", "", "The GCP Project ID.")
	prefix := flag.String("prefix", "", "The prefix of service accounts to clean (e.g., 'it-').")
	flag.Parse()

	if *projectID == "" || *prefix == "" {
		fmt.Println("Usage: go run cleanup_sa_roles.go -projectID=<your-project> -prefix=<sa-prefix>")
		os.Exit(1)
	}

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	log.Info().Str("projectID", *projectID).Str("prefix", *prefix).Msg("Starting service account cleanup...")

	ctx := context.Background()

	// --- Create the IAM manager from your project ---
	saManager, err := iam.NewServiceAccountManager(ctx, *projectID, log.Logger)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create ServiceAccountManager")
	}
	defer func() {
		_ = saManager.Close()
	}()

	// --- Find and clean the accounts ---
	allAccounts, err := saManager.ListServiceAccounts(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to list service accounts")
	}

	cleanedCount := 0
	for _, sa := range allAccounts {
		accountID := strings.Split(sa.Email, "@")[0]
		if !strings.HasPrefix(accountID, *prefix) {
			continue // Skip accounts that don't match the prefix
		}

		log.Info().Str("email", sa.Email).Msg("Checking account...")

		// Get the current policy for the service account.
		policy, err := saManager.GetServiceAccountIAMPolicy(ctx, sa.Email)
		if err != nil {
			log.Error().Err(err).Str("email", sa.Email).Msg("Could not get policy, skipping.")
			continue
		}

		// If there are no roles, it's already clean.
		if len(policy.InternalProto.Bindings) == 0 {
			log.Info().Str("email", sa.Email).Msg("Account is already clean.")
			continue
		}

		// Create a new, empty policy, preserving the Etag to prevent race conditions.
		log.Warn().Str("email", sa.Email).Int("role_count", len(policy.InternalProto.Bindings)).Msg("Account has roles, cleaning now...")
		newPolicy := &giam.Policy{
			InternalProto: &iampb.Policy{
				Etag: policy.InternalProto.Etag,
			},
		}

		// Set the new empty policy, effectively stripping all roles.
		err = saManager.SetServiceAccountIAMPolicy(ctx, sa.Email, newPolicy)
		if err != nil {
			log.Error().Err(err).Str("email", sa.Email).Msg("Failed to set clean policy.")
		} else {
			log.Info().Str("email", sa.Email).Msg("✅ Successfully cleaned account.")
			cleanedCount++
		}
	}

	log.Info().Int("cleaned_count", cleanedCount).Msg("Cleanup complete.")
}
