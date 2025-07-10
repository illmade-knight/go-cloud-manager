// File: pkg/cloudmanager/alerts_pubsub.go
// This file contains the specific logic for creating Pub/Sub alerts.

package cloudmanager

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// PubSubBacklogAlertConfig contains the parameters for an alert on the number of unacknowledged Pub/Sub messages.
// The alert triggers if the number of undelivered messages stays above the BacklogThreshold for the entire SustainPeriod.
//
// Example 1: Critical Backlog Alert (Fast Trigger)
// This is useful for catching a subscriber that has stopped processing messages completely.
//
//		config := PubSubBacklogAlertConfig{
//		    ProjectID:                   "production-project",
//	     SubscriptionID:              "my-critical-subscription",
//		    AlertName:                   "PubSub - CRITICAL Backlog on my-critical-subscription",
//		    BacklogThreshold:            1000, // Alert if more than 1000 messages are waiting...
//		    SustainPeriod:               5 * time.Minute, // ...and the backlog persists for 5 minutes.
//		    NotificationChannelResource: "projects/production-project/notificationChannels/12345",
//		}
//
// Example 2: Slow Consumer Warning
// This is for catching subscribers that are processing messages, but too slowly to keep up with the incoming rate.
//
//		config := PubSubBacklogAlertConfig{
//		    ProjectID:                   "production-project",
//	     SubscriptionID:              "my-batch-job-subscription",
//		    AlertName:                   "PubSub - WARNING Slow Consumer on my-batch-job-subscription",
//		    BacklogThreshold:            100, // Alert if more than 100 messages are waiting...
//		    SustainPeriod:               1 * time.Hour, // ...and the backlog persists for a full hour.
//		    NotificationChannelResource: "projects/production-project/notificationChannels/67890",
//		}
type PubSubBacklogAlertConfig struct {
	ProjectID                   string
	SubscriptionID              string
	AlertName                   string
	BacklogThreshold            float64
	SustainPeriod               time.Duration
	NotificationChannelResource string
}

// TopicLowPublishRateAlertConfig contains the parameters for an alert when a topic's publish rate drops.
// The alert triggers if the total number of published messages over the EvaluationPeriod is less than
// the MinMessagesPerPeriod threshold.
//
// Example: Detect a silent producer
// This is useful for ensuring a critical data producer hasn't silently stopped sending data.
//
//	config := TopicLowPublishRateAlertConfig{
//	    ProjectID:                   "production-project",
//	    TopicID:                     "my-heartbeat-topic",
//	    AlertName:                   "PubSub - CRITICAL No messages on my-heartbeat-topic",
//	    MinMessagesPerPeriod:        1, // Alert if we receive less than 1 message...
//	    EvaluationPeriod:            15 * time.Minute, // ...over a 15-minute period.
//	    NotificationChannelResource: "projects/production-project/notificationChannels/12345",
//	}
type TopicLowPublishRateAlertConfig struct {
	ProjectID                   string
	TopicID                     string
	AlertName                   string
	MinMessagesPerPeriod        float64
	EvaluationPeriod            time.Duration
	NotificationChannelResource string
}

// CreatePubSubBacklogAlert creates an alerting policy for a high number of unacknowledged messages.
func (m *Manager) CreatePubSubBacklogAlert(ctx context.Context, config PubSubBacklogAlertConfig) (*monitoringpb.AlertPolicy, error) {
	m.Logger.Printf("Creating Pub/Sub alert policy '%s'...", config.AlertName)

	filter := fmt.Sprintf(`metric.type = "pubsub.googleapis.com/subscription/num_undelivered_messages" AND resource.labels.subscription_id = "%s"`, config.SubscriptionID)

	condition := &monitoringpb.AlertPolicy_Condition{
		DisplayName: fmt.Sprintf("Unacknowledged messages > %v for %v", int(config.BacklogThreshold), config.SustainPeriod),
		Condition: &monitoringpb.AlertPolicy_Condition_ConditionThreshold{
			ConditionThreshold: &monitoringpb.AlertPolicy_Condition_MetricThreshold{
				Filter:         filter,
				Comparison:     monitoringpb.ComparisonType_COMPARISON_GT,
				ThresholdValue: config.BacklogThreshold,
				Duration:       durationpb.New(config.SustainPeriod),
				Trigger: &monitoringpb.AlertPolicy_Condition_Trigger{
					Type: &monitoringpb.AlertPolicy_Condition_Trigger_Count{
						Count: 1,
					},
				},
			},
		},
	}

	policy := &monitoringpb.AlertPolicy{
		DisplayName: config.AlertName,
		Combiner:    monitoringpb.AlertPolicy_AND,
		Conditions:  []*monitoringpb.AlertPolicy_Condition{condition},
		NotificationChannels: []string{
			config.NotificationChannelResource,
		},
		Documentation: &monitoringpb.AlertPolicy_Documentation{
			Content:  fmt.Sprintf("The number of unacknowledged messages for subscription '%s' has exceeded the threshold. This indicates that subscribers may not be processing messages correctly or are falling behind.", config.SubscriptionID),
			MimeType: "text/markdown",
		},
	}

	req := &monitoringpb.CreateAlertPolicyRequest{
		Name:        "projects/" + config.ProjectID,
		AlertPolicy: policy,
	}

	createdPolicy, err := m.AlertingClient.CreateAlertPolicy(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create Pub/Sub alert policy: %w", err)
	}

	m.Logger.Printf("Successfully created alert policy: %s", createdPolicy.Name)
	return createdPolicy, nil
}

// CreateTopicLowPublishRateAlert creates an alerting policy for a low rate of published messages on a topic.
func (m *Manager) CreateTopicLowPublishRateAlert(ctx context.Context, config TopicLowPublishRateAlertConfig) (*monitoringpb.AlertPolicy, error) {
	m.Logger.Printf("Creating Pub/Sub Topic alert policy '%s'...", config.AlertName)

	// This metric counts the number of publish operations.
	filter := fmt.Sprintf(`metric.type = "pubsub.googleapis.com/topic/send_request_count" AND resource.labels.topic_id = "%s"`, config.TopicID)

	condition := &monitoringpb.AlertPolicy_Condition{
		DisplayName: fmt.Sprintf("Published messages < %v over %v", int(config.MinMessagesPerPeriod), config.EvaluationPeriod),
		Condition: &monitoringpb.AlertPolicy_Condition_ConditionThreshold{
			ConditionThreshold: &monitoringpb.AlertPolicy_Condition_MetricThreshold{
				Filter:         filter,
				Comparison:     monitoringpb.ComparisonType_COMPARISON_LT, // Less Than
				ThresholdValue: config.MinMessagesPerPeriod,
				Duration:       durationpb.New(config.EvaluationPeriod),
				Trigger: &monitoringpb.AlertPolicy_Condition_Trigger{
					Type: &monitoringpb.AlertPolicy_Condition_Trigger_Count{
						Count: 1,
					},
				},
				// Sum up all publish requests over the evaluation period.
				Aggregations: []*monitoringpb.Aggregation{
					{
						AlignmentPeriod:    durationpb.New(config.EvaluationPeriod),
						PerSeriesAligner:   monitoringpb.Aggregation_ALIGN_SUM,
						CrossSeriesReducer: monitoringpb.Aggregation_REDUCE_SUM,
					},
				},
			},
		},
	}

	policy := &monitoringpb.AlertPolicy{
		DisplayName: config.AlertName,
		Combiner:    monitoringpb.AlertPolicy_AND,
		Conditions:  []*monitoringpb.AlertPolicy_Condition{condition},
		NotificationChannels: []string{
			config.NotificationChannelResource,
		},
		Documentation: &monitoringpb.AlertPolicy_Documentation{
			Content:  fmt.Sprintf("The rate of messages published to topic '%s' has dropped below the threshold. This indicates a potential issue with an upstream message producer.", config.TopicID),
			MimeType: "text/markdown",
		},
	}

	req := &monitoringpb.CreateAlertPolicyRequest{
		Name:        "projects/" + config.ProjectID,
		AlertPolicy: policy,
	}

	createdPolicy, err := m.AlertingClient.CreateAlertPolicy(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create Pub/Sub topic alert policy: %w", err)
	}

	m.Logger.Printf("Successfully created alert policy: %s", createdPolicy.Name)
	return createdPolicy, nil
}
