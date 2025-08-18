package main

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
)

// This program runs LOCALLY to create or update multiple secrets in one go.
func main() {
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
	defer func() {
		_ = client.Close()
	}()

	var wg sync.WaitGroup

	for _, pair := range secretPairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			log.Printf("WARN: Skipping invalid argument format: %s", pair)
			continue
		}
		secretID := parts[0]
		secretValue := parts[1]

		wg.Add(1)
		go func(id, value string) {
			defer wg.Done()
			// CORRECTED: Call the new, shared function.
			result, err := servicemanager.EnsureSecretExistsWithValue(ctx, client, projectID, id, value)
			if err != nil {
				log.Printf("ERROR: Failed to set secret '%s': %v", id, err)
				return
			}
			log.Printf("SUCCESS: Set secret '%s'. New version is: %s", id, result.Name)
		}(secretID, secretValue)
	}

	wg.Wait()
	log.Println("✅ All secrets processed.")
}
