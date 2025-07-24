package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// This program runs LOCALLY to create or update multiple secrets in one go.
func main() {
	// Expects: go run . <project-id> SECRET_NAME1=VALUE1 SECRET_NAME2=VALUE2 ...
	if len(os.Args) < 3 {
		log.Fatal("Usage: go run . <project-id> [SECRET_NAME=VALUE...]")
	}
	projectID := os.Args[1]
	secretPairs := os.Args[2:]

	ctx := context.Background()
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		log.Fatalf("Failed to create secretmanager client: %v", err)
	}
	defer client.Close()

	// Use a WaitGroup to process all secrets concurrently.
	var wg sync.WaitGroup

	for _, pair := range secretPairs {
		// Parse the "KEY=VALUE" string.
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			log.Printf("WARN: Skipping invalid argument format: %s", pair)
			continue
		}
		secretID := parts[0]
		secretValue := parts[1]

		wg.Add(1)
		// Launch a goroutine to set each secret.
		go func(id, value string) {
			defer wg.Done()
			setSecret(ctx, client, projectID, id, value)
		}(secretID, secretValue)
	}

	// Wait for all goroutines to finish.
	wg.Wait()
	log.Println("✅ All secrets processed.")
}

// setSecret creates a secret if it doesn't exist, then adds the new value as a version.
func setSecret(ctx context.Context, client *secretmanager.Client, projectID, secretID, secretValue string) {
	parent := fmt.Sprintf("projects/%s", projectID)
	secretPath := fmt.Sprintf("%s/secrets/%s", parent, secretID)

	// Check if the secret exists. If not, create it.
	_, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: secretPath})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			log.Printf("INFO: Secret '%s' not found, creating it...", secretID)
			_, createErr := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
				Parent:   parent,
				SecretId: secretID,
				Secret: &secretmanagerpb.Secret{
					Replication: &secretmanagerpb.Replication{
						Replication: &secretmanagerpb.Replication_Automatic_{
							Automatic: &secretmanagerpb.Replication_Automatic{},
						},
					},
				},
			})
			if createErr != nil {
				log.Printf("ERROR: Failed to create secret '%s': %v", secretID, createErr)
				return
			}
		} else {
			log.Printf("ERROR: Failed to check for secret '%s': %v", secretID, err)
			return
		}
	}

	// Add a new version with the secret value. This is the SET operation.
	addVersionReq := &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretPath,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte(secretValue)},
	}
	result, err := client.AddSecretVersion(ctx, addVersionReq)
	if err != nil {
		log.Printf("ERROR: Failed to add version to secret '%s': %v", secretID, err)
		return
	}

	log.Printf("SUCCESS: Set secret '%s'. New version is: %s", secretID, result.Name)
}
