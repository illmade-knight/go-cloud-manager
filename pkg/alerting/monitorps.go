// =================================================================
// File: pkg/monitoring/monitoring.go
// This file contains the core library logic.
// =================================================================

package cloudmanager

import (
	"context"
	"fmt"
	"log"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MonitoredSubscription defines a Pub/Sub subscription to monitor
// along with its specific alerting rules.
type MonitoredSubscription struct {
	ProjectID             string
	SubscriptionID        string
	MinAckMessagesPerHour int64
}

// MonitoredTopic defines a Pub/Sub topic to monitor.
type MonitoredTopic struct {
	ProjectID string
	TopicID   string
}

// Config holds the full monitoring configuration for the service.
type Config struct {
	Subscriptions []MonitoredSubscription
	Topics        []MonitoredTopic
	CheckInterval time.Duration
}

// Monitor is the main struct for the monitoring service.
type Monitor struct {
	client *monitoring.MetricClient
	config Config
	logger *log.Logger
}

// New creates a new Monitor instance.
func New(ctx context.Context, cfg Config, logger *log.Logger) (*Monitor, error) {
	// This uses Application Default Credentials to authenticate.
	// Ensure you have authenticated via `gcloud auth application-default login`.
	c, err := monitoring.NewMetricClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create metric client: %w", err)
	}

	return &Monitor{
		client: c,
		config: cfg,
		logger: logger,
	}, nil
}

// Run starts the main monitoring loop. It's a blocking call.
func (m *Monitor) Run(ctx context.Context) error {
	m.logger.Println("Pub/Sub monitoring service started.")

	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	// Run the first check immediately
	m.performCheck(ctx)

	for {
		select {
		case <-ticker.C:
			m.performCheck(ctx)
		case <-ctx.Done():
			m.logger.Println("Shutting down monitoring service.")
			m.client.Close()
			return ctx.Err()
		}
	}
}

// performCheck runs a single monitoring check across all configured resources.
func (m *Monitor) performCheck(ctx context.Context) {
	m.logger.Println("Running monitoring check...")
	for _, sub := range m.config.Subscriptions {
		err := m.checkSubscription(ctx, sub)
		if err != nil {
			m.logger.Printf("Error checking subscription %s: %v", sub.SubscriptionID, err)
		}
	}
	// Future: Add a call to a function for checking topics here.
}

// checkSubscription fetches metrics for a single subscription and evaluates its rules.
func (m *Monitor) checkSubscription(ctx context.Context, sub MonitoredSubscription) error {
	now := time.Now()
	startTime := now.Add(-1 * time.Hour)

	req := &monitoringpb.ListTimeSeriesRequest{
		Name:   "projects/" + sub.ProjectID,
		Filter: fmt.Sprintf(`metric.type = "pubsub.googleapis.com/subscription/ack_message_count" AND resource.labels.subscription_id = "%s"`, sub.SubscriptionID),
		Interval: &monitoringpb.TimeInterval{
			StartTime: timestamppb.New(startTime),
			EndTime:   timestamppb.New(now),
		},
		Aggregation: &monitoringpb.Aggregation{
			AlignmentPeriod:    &durationpb.Duration{Seconds: 3600}, // 1 hour
			PerSeriesAligner:   monitoringpb.Aggregation_ALIGN_SUM,
			CrossSeriesReducer: monitoringpb.Aggregation_REDUCE_SUM,
		},
	}

	it := m.client.ListTimeSeries(ctx, req)

	result, err := it.Next()
	if err == iterator.Done {
		if sub.MinAckMessagesPerHour > 0 {
			m.logger.Printf("ALERT: No messages acknowledged for subscription '%s' in the last hour (expected at least %d).", sub.SubscriptionID, sub.MinAckMessagesPerHour)
		} else {
			m.logger.Printf("INFO: No messages acknowledged for subscription '%s' in the last hour.", sub.SubscriptionID)
		}
		// Here you would trigger a real alert (e.g., call PagerDuty, send a Slack message).
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not read time series value: %w", err)
	}

	if len(result.Points) > 0 {
		acknowledgedMessages := result.Points[0].GetValue().GetInt64Value()
		m.logger.Printf("INFO: Subscription '%s' acknowledged %d messages in the last hour.", sub.SubscriptionID, acknowledgedMessages)

		if acknowledgedMessages < sub.MinAckMessagesPerHour {
			m.logger.Printf("ALERT: Subscription '%s' has low message acknowledgement rate. Expected at least %d, but got %d.",
				sub.SubscriptionID, sub.MinAckMessagesPerHour, acknowledgedMessages)
			// Trigger a real alert here.
		}
	} else {
		m.logger.Printf("INFO: No data points returned for subscription '%s' in the specified time frame.", sub.SubscriptionID)
		if sub.MinAckMessagesPerHour > 0 {
			m.logger.Printf("ALERT: Subscription '%s' has low message acknowledgement rate. Expected at least %d, but got 0.",
				sub.SubscriptionID, sub.MinAckMessagesPerHour)
		}
	}

	return nil
}
