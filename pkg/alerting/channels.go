// File: pkg/cloudmanager/channels.go
// This file contains the specific logic for creating notification channels.

package cloudmanager

import (
	"context"
	"fmt"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
)

// EmailChannelConfig contains the parameters for creating an email notification channel.
type EmailChannelConfig struct {
	ProjectID    string
	EmailAddress string
	DisplayName  string
}

// CreateEmailChannel creates a new email notification channel in the specified project.
func (m *Manager) CreateEmailChannel(ctx context.Context, config EmailChannelConfig) (*monitoringpb.NotificationChannel, error) {
	m.Logger.Printf("Creating email notification channel '%s' for %s...", config.DisplayName, config.EmailAddress)

	channel := &monitoringpb.NotificationChannel{
		Type:        "email",
		DisplayName: config.DisplayName,
		Labels: map[string]string{
			"email_address": config.EmailAddress,
		},
	}

	req := &monitoringpb.CreateNotificationChannelRequest{
		Name:                "projects/" + config.ProjectID,
		NotificationChannel: channel,
	}

	createdChannel, err := m.NotificationChannelClient.CreateNotificationChannel(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create email notification channel: %w", err)
	}

	m.Logger.Printf("Successfully created notification channel: %s", createdChannel.Name)
	return createdChannel, nil
}
