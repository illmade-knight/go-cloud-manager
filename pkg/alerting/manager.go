// File: pkg/cloudmanager/manager.go
// This is the core of the package, providing a central manager
// for interacting with various Google Cloud services.

package cloudmanager

import (
	"context"
	"fmt"
	"log"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
)

// Manager provides a unified client for interacting with Google Cloud's
// monitoring, alerting, and notification channel APIs.
type Manager struct {
	AlertingClient            *monitoring.AlertPolicyClient
	MonitoringClient          *monitoring.MetricClient
	NotificationChannelClient *monitoring.NotificationChannelClient
	Logger                    *log.Logger
}

// NewManager creates a new Manager instance, initializing all required clients.
func NewManager(ctx context.Context, logger *log.Logger) (*Manager, error) {
	alertClient, err := monitoring.NewAlertPolicyClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create alert policy client: %w", err)
	}

	metricClient, err := monitoring.NewMetricClient(ctx)
	if err != nil {
		alertClient.Close() // Clean up already created client
		return nil, fmt.Errorf("failed to create metric client: %w", err)
	}

	channelClient, err := monitoring.NewNotificationChannelClient(ctx)
	if err != nil {
		alertClient.Close()
		metricClient.Close()
		return nil, fmt.Errorf("failed to create notification channel client: %w", err)
	}

	return &Manager{
		AlertingClient:            alertClient,
		MonitoringClient:          metricClient,
		NotificationChannelClient: channelClient,
		Logger:                    logger,
	}, nil
}

// Close gracefully closes all underlying client connections.
func (m *Manager) Close() {
	if m.AlertingClient != nil {
		m.AlertingClient.Close()
	}
	if m.MonitoringClient != nil {
		m.MonitoringClient.Close()
	}
	if m.NotificationChannelClient != nil {
		m.NotificationChannelClient.Close()
	}
}
