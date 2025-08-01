package servicemanager

import (
	"context"
	"fmt"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnsureSecretExistsWithValue ensures a secret with the given ID exists, creating it if necessary,
// and then adds the provided value as the newest version of the secret.
// This function is idempotent and safe to call multiple times. Each call will add a new version
// with the same value, which is useful for audit trails but may incur costs.
//
// Parameters:
//   - ctx: The context for the API call.
//   - client: An initialized Secret Manager client.
//   - projectID: The Google Cloud project ID.
//   - secretID: The unique ID for the secret (e.g., "my-api-key").
//   - secretValue: The string value to store in the new secret version.
//
// Returns the newly created secret version or an error.
func EnsureSecretExistsWithValue(ctx context.Context, client *secretmanager.Client, projectID, secretID, secretValue string) (*secretmanagerpb.SecretVersion, error) {
	parent := fmt.Sprintf("projects/%s", projectID)
	secretPath := fmt.Sprintf("%s/secrets/%s", parent, secretID)

	// First, check if the secret container already exists.
	_, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: secretPath})
	if err != nil {
		// If the error is "NotFound", we need to create the secret.
		if status.Code(err) == codes.NotFound {
			// Create the secret with automatic replication for simplicity.
			createReq := &secretmanagerpb.CreateSecretRequest{
				Parent:   parent,
				SecretId: secretID,
				Secret: &secretmanagerpb.Secret{
					Replication: &secretmanagerpb.Replication{
						Replication: &secretmanagerpb.Replication_Automatic_{
							Automatic: &secretmanagerpb.Replication_Automatic{},
						},
					},
				},
			}
			_, createErr := client.CreateSecret(ctx, createReq)
			if createErr != nil {
				return nil, fmt.Errorf("failed to create secret '%s': %w", secretID, createErr)
			}
		} else {
			// Any other error during the check is a problem.
			return nil, fmt.Errorf("failed to check for secret '%s': %w", secretID, err)
		}
	}

	// Now that the secret container is guaranteed to exist, add the new value as a version.
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
