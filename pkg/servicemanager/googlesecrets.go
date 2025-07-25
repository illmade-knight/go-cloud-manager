package servicemanager

import (
	"context"
	"fmt"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnsureSecretExistsWithValue creates a secret if it doesn't exist, then adds the new value as the latest version.
// This function is repeatable and safe to call multiple times.
func EnsureSecretExistsWithValue(ctx context.Context, client *secretmanager.Client, projectID, secretID, secretValue string) (*secretmanagerpb.SecretVersion, error) {
	parent := fmt.Sprintf("projects/%s", projectID)
	secretPath := fmt.Sprintf("%s/secrets/%s", parent, secretID)

	// Check if the secret exists. If not, create it.
	_, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: secretPath})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Create the secret with automatic replication.
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
				return nil, fmt.Errorf("failed to create secret '%s': %w", secretID, createErr)
			}
		} else {
			return nil, fmt.Errorf("failed to check for secret '%s': %w", secretID, err)
		}
	}

	// Add the new value as a secret version.
	addVersionReq := &secretmanagerpb.AddSecretVersionRequest{
		Parent:  secretPath,
		Payload: &secretmanagerpb.SecretPayload{Data: []byte(secretValue)},
	}
	result, err := client.AddSecretVersion(ctx, addVersionReq)
	if err != nil {
		return nil, fmt.Errorf("failed to add version to secret '%s': %w", secretID, err)
	}

	return result, nil
}
