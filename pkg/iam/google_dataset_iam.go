package iam

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/bigquery"
)

// BigQueryIAMManager provides a dedicated client for managing IAM policies on BigQuery Datasets.
// It correctly uses the dataset metadata update mechanism with the IAMMemberEntity type.
// We map resource level IAM to dataset's more legacy roles in our delete and verify methods
// NOTE: we should only really grant dataset level access to humans (data scientists etc.) - for
// services the access should be at table level only
type BigQueryIAMManager struct {
	client *bigquery.Client
}

// NewBigQueryIAMManager creates a new, correctly configured manager for BigQuery dataset IAM.
func NewBigQueryIAMManager(client *bigquery.Client) (*BigQueryIAMManager, error) {
	if client == nil {
		return nil, fmt.Errorf("bigquery client cannot be nil")
	}
	return &BigQueryIAMManager{
		client: client,
	}, nil
}

// translateToLegacyRole converts a modern IAM role string to the corresponding legacy bigquery.AccessRole.
// This is required because the BigQuery API translates modern IAM roles into legacy roles when storing them
// in a dataset's ACL. Our verification logic must check for the translated legacy role.
func translateToLegacyRole(role string) (bigquery.AccessRole, error) {
	switch role {
	case "roles/bigquery.dataViewer", "roles/bigquery.user":
		return bigquery.ReaderRole, nil
	case "roles/bigquery.dataEditor", "roles/bigquery.dataOwner":
		return bigquery.WriterRole, nil
	default:
		// Return the original role if it doesn't match a known translation.
		// This handles legacy roles like OWNER being passed in directly.
		return bigquery.AccessRole(role), nil
	}
}

// AddDatasetIAMBinding grants a role to a member on a specific BigQuery dataset.
// This implementation correctly uses the IAMMemberEntity type. When the API receives this,
// it translates it into a legacy ACL entry.
func (m *BigQueryIAMManager) AddDatasetIAMBinding(ctx context.Context, datasetID, role, member string) error {
	dataset := m.client.Dataset(datasetID)
	meta, err := dataset.Metadata(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dataset metadata: %w", err)
	}

	// First, check if an equivalent legacy permission already exists.
	found, err := m.CheckDatasetIAMBinding(ctx, datasetID, role, member)
	if err != nil {
		return err
	}
	if found {
		return nil // Idempotent: an equivalent permission already exists.
	}

	// If not found, add the new permission using the modern IAM format.
	update := bigquery.DatasetMetadataToUpdate{
		Access: append(meta.Access, &bigquery.AccessEntry{
			Role:       bigquery.AccessRole(role),
			EntityType: bigquery.IAMMemberEntity,
			Entity:     member,
		}),
	}

	if _, err := dataset.Update(ctx, update, meta.ETag); err != nil {
		return fmt.Errorf("failed to update dataset ACL: %w", err)
	}
	return nil
}

// RemoveDatasetIAMBinding removes a role from a member on a specific BigQuery dataset.
// It must translate the modern role to its legacy equivalent to find the correct entry to remove.
func (m *BigQueryIAMManager) RemoveDatasetIAMBinding(ctx context.Context, datasetID, role, member string) error {
	dataset := m.client.Dataset(datasetID)
	meta, err := dataset.Metadata(ctx)
	if err != nil {
		return err
	}

	legacyRole, _ := translateToLegacyRole(role)
	// The translated entity is a bare email, associated with UserEmailEntity.
	entity := strings.TrimPrefix(member, "serviceAccount:")

	var newAccessList []*bigquery.AccessEntry
	var modified bool
	for _, entry := range meta.Access {
		// Look for the translated legacy entry to remove it.
		if entry.Role == legacyRole && entry.EntityType == bigquery.UserEmailEntity && entry.Entity == entity {
			modified = true
			continue
		}
		newAccessList = append(newAccessList, entry)
	}

	if !modified {
		return nil // Entry didn't exist, nothing to remove.
	}

	update := bigquery.DatasetMetadataToUpdate{
		Access: newAccessList,
	}

	if _, err := dataset.Update(ctx, update, meta.ETag); err != nil {
		return err
	}
	return nil
}

// CheckDatasetIAMBinding verifies if a member has a specific role on a BigQuery dataset.
// This check is now "translation-aware". It checks for the legacy ACL entry that the BigQuery API
// creates when it receives a modern IAM role.
func (m *BigQueryIAMManager) CheckDatasetIAMBinding(ctx context.Context, datasetID, role, member string) (bool, error) {
	dataset := m.client.Dataset(datasetID)
	meta, err := dataset.Metadata(ctx)
	if err != nil {
		return false, err
	}

	// Translate the modern role and member to their legacy ACL equivalents.
	legacyRole, _ := translateToLegacyRole(role)
	entity := strings.TrimPrefix(member, "serviceAccount:")

	// Search the ACL for the translated legacy entry.
	for _, entry := range meta.Access {
		if entry.Role == legacyRole && entry.EntityType == bigquery.UserEmailEntity && entry.Entity == entity {
			return true, nil
		}
	}

	return false, nil
}

func (m *BigQueryIAMManager) Close() error {
	return m.client.Close()
}
