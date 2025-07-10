// File: pkg/cloudmanager/alerts_bigquery.go
// This file contains the specific logic for creating BigQuery alerts.

package cloudmanager

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// BigQueryErrorAlertConfig contains the parameters for an alert on the number of failed BigQuery jobs.
// The alert triggers if the sum of failed queries over the EvaluationPeriod is greater than the
// ErrorCountThreshold.
//
// Example 1: Alert on a quick burst of errors.
// This is useful for catching sudden, high-impact failures.
//
//	config := BigQueryErrorAlertConfig{
//	    ProjectID:                   "production-project",
//	    AlertName:                   "BigQuery - High Failure Rate (Burst)",
//	    ErrorCountThreshold:         10, // Alert if we get more than 10 errors...
//	    EvaluationPeriod:            5 * time.Minute, // ...within any 5-minute period.
//	    NotificationChannelResource: "projects/production-project/notificationChannels/12345",
//	}
//
// Example 2: Alert on a slow but persistent problem.
// This is for catching "low and slow" issues that might not cause a huge spike.
//
//	config := BigQueryErrorAlertConfig{
//	    ProjectID:                   "production-project",
//	    AlertName:                   "BigQuery - Persistent Low-Level Errors",
//	    ErrorCountThreshold:         0, // Alert if the error count is greater than zero...
//	    EvaluationPeriod:            1 * time.Hour, // ...and the condition persists for a full hour.
//	    NotificationChannelResource: "projects/production-project/notificationChannels/12345",
//	}
type BigQueryErrorAlertConfig struct {
	ProjectID                   string
	AlertName                   string
	ErrorCountThreshold         float64
	EvaluationPeriod            time.Duration
	NotificationChannelResource string
}

// CreateBigQueryErrorAlert creates an alerting policy for failed BigQuery queries using the provided config.
func (m *Manager) CreateBigQueryErrorAlert(ctx context.Context, config BigQueryErrorAlertConfig) (*monitoringpb.AlertPolicy, error) {
	m.Logger.Printf("Creating BigQuery alert policy '%s'...", config.AlertName)

	filter := `metric.type = "bigquery.googleapis.com/query/count" AND metric.labels.execution_status = "error" AND resource.type = "bigquery_project"`

	condition := &monitoringpb.AlertPolicy_Condition{
		DisplayName: fmt.Sprintf("BigQuery query jobs failed > %v over %v", int(config.ErrorCountThreshold), config.EvaluationPeriod),
		Condition: &monitoringpb.AlertPolicy_Condition_ConditionThreshold{
			ConditionThreshold: &monitoringpb.AlertPolicy_Condition_MetricThreshold{
				Filter:         filter,
				Comparison:     monitoringpb.ComparisonType_COMPARISON_GT,
				ThresholdValue: config.ErrorCountThreshold,
				Duration:       durationpb.New(config.EvaluationPeriod),
				Trigger: &monitoringpb.AlertPolicy_Condition_Trigger{
					Type: &monitoringpb.AlertPolicy_Condition_Trigger_Count{
						Count: 1,
					},
				},
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
			Content:  "The number of failed BigQuery queries has exceeded the threshold. This could indicate issues with data pipelines, user-submitted queries, or underlying data.",
			MimeType: "text/markdown",
		},
	}

	req := &monitoringpb.CreateAlertPolicyRequest{
		Name:        "projects/" + config.ProjectID,
		AlertPolicy: policy,
	}

	createdPolicy, err := m.AlertingClient.CreateAlertPolicy(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create alert policy: %w", err)
	}

	m.Logger.Printf("Successfully created alert policy: %s", createdPolicy.Name)
	return createdPolicy, nil
}
