package servicemanager

import (
	"cloud.google.com/go/pubsub"
	"context"
	"fmt"
	"google.golang.org/api/option"
	"reflect"
	"time"
)

const (
	// Google Cloud Pub/Sub constraints
	minAckDeadline = 10 * time.Second
	maxAckDeadline = 600 * time.Second
	minRetention   = 10 * time.Minute
	maxRetention   = 7 * 24 * time.Hour
)

// --- Conversion Helpers ---

func fromGCPTopicConfig(t *pubsub.TopicConfig) *TopicConfig {
	return &TopicConfig{
		CloudResource: CloudResource{
			Name:   t.ID(),
			Labels: t.Labels,
		},
	}
}

func fromGCPSubscriptionConfig(s *pubsub.SubscriptionConfig) *SubscriptionConfig {
	spec := &SubscriptionConfig{
		CloudResource: CloudResource{
			Name:   s.ID(),
			Labels: s.Labels,
		},
		Topic:              s.Topic.ID(),
		AckDeadlineSeconds: int(s.AckDeadline.Seconds()),
		MessageRetention:   Duration(s.RetentionDuration),
	}
	// RetryPolicy is now ignored for simplicity.
	return spec
}

// --- Adapter Implementations ---

type gcpTopicAdapter struct{ topic *pubsub.Topic }

func (a *gcpTopicAdapter) ID() string                               { return a.topic.ID() }
func (a *gcpTopicAdapter) Exists(ctx context.Context) (bool, error) { return a.topic.Exists(ctx) }
func (a *gcpTopicAdapter) Update(ctx context.Context, cfg TopicConfig) (*TopicConfig, error) {
	gcpUpdate := pubsub.TopicConfigToUpdate{
		Labels: cfg.Labels,
	}
	updatedCfg, err := a.topic.Update(ctx, gcpUpdate)
	if err != nil {
		return nil, err
	}
	return fromGCPTopicConfig(&updatedCfg), nil
}
func (a *gcpTopicAdapter) Delete(ctx context.Context) error { return a.topic.Delete(ctx) }

type gcpSubscriptionAdapter struct{ sub *pubsub.Subscription }

func (a *gcpSubscriptionAdapter) ID() string                               { return a.sub.ID() }
func (a *gcpSubscriptionAdapter) Exists(ctx context.Context) (bool, error) { return a.sub.Exists(ctx) }

// Update contains the "fetch, compare, execute" logic, specific to Google Pub/Sub.
func (a *gcpSubscriptionAdapter) Update(ctx context.Context, spec SubscriptionConfig) (*SubscriptionConfig, error) {
	currentGcpCfg, err := a.sub.Config(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current config for subscription '%s': %w", spec.Name, err)
	}
	currentCfg := fromGCPSubscriptionConfig(&currentGcpCfg)

	gcpUpdate := pubsub.SubscriptionConfigToUpdate{}
	needsUpdate := false

	// Compare AckDeadline
	if spec.AckDeadlineSeconds > 0 && spec.AckDeadlineSeconds != currentCfg.AckDeadlineSeconds {
		gcpUpdate.AckDeadline = time.Duration(spec.AckDeadlineSeconds) * time.Second
		needsUpdate = true
	}

	// Compare MessageRetention
	if spec.MessageRetention > 0 && spec.MessageRetention != currentCfg.MessageRetention {
		gcpUpdate.RetentionDuration = time.Duration(spec.MessageRetention)
		needsUpdate = true
	}

	// Compare Labels
	if !reflect.DeepEqual(spec.Labels, currentCfg.Labels) {
		gcpUpdate.Labels = spec.Labels
		needsUpdate = true
	}

	// Compare RetryPolicy
	if !reflect.DeepEqual(spec.RetryPolicy, currentCfg.RetryPolicy) {
		if spec.RetryPolicy != nil {
			gcpUpdate.RetryPolicy = &pubsub.RetryPolicy{
				MinimumBackoff: spec.RetryPolicy.MinimumBackoff,
				MaximumBackoff: spec.RetryPolicy.MaximumBackoff,
			}
		} else {
			// A nil spec policy means we are clearing the existing one.
			gcpUpdate.RetryPolicy = &pubsub.RetryPolicy{}
		}
		needsUpdate = true
	}

	if !needsUpdate {
		// Nothing to do, return the current config.
		return currentCfg, nil
	}

	updatedGcpCfg, err := a.sub.Update(ctx, gcpUpdate)
	if err != nil {
		return nil, err
	}
	return fromGCPSubscriptionConfig(&updatedGcpCfg), nil
}

func (a *gcpSubscriptionAdapter) Delete(ctx context.Context) error { return a.sub.Delete(ctx) }

type gcpMessagingClientAdapter struct{ client *pubsub.Client }

func (a *gcpMessagingClientAdapter) Topic(id string) MessagingTopic {
	return &gcpTopicAdapter{topic: a.client.Topic(id)}
}

func (a *gcpMessagingClientAdapter) Subscription(id string) MessagingSubscription {
	return &gcpSubscriptionAdapter{sub: a.client.Subscription(id)}
}

func (a *gcpMessagingClientAdapter) CreateTopic(ctx context.Context, topicID string) (MessagingTopic, error) {
	t, err := a.client.CreateTopic(ctx, topicID)
	if err != nil {
		return nil, err
	}
	return &gcpTopicAdapter{topic: t}, nil
}
func (a *gcpMessagingClientAdapter) CreateTopicWithConfig(ctx context.Context, topicSpec TopicConfig) (MessagingTopic, error) {
	gcpConfig := &pubsub.TopicConfig{
		Labels: topicSpec.Labels,
	}
	t, err := a.client.CreateTopicWithConfig(ctx, topicSpec.Name, gcpConfig)
	if err != nil {
		return nil, err
	}
	return &gcpTopicAdapter{topic: t}, nil
}

func (a *gcpMessagingClientAdapter) CreateSubscription(ctx context.Context, subSpec SubscriptionConfig) (MessagingSubscription, error) {
	// The check for topic existence has been REMOVED from the adapter.
	// This responsibility now lies solely with the MessagingManager orchestrator,
	// which simplifies the adapter and avoids a redundant API call.
	topic := a.client.Topic(subSpec.Topic)

	gcpConfig := pubsub.SubscriptionConfig{
		Topic:             topic,
		Labels:            subSpec.Labels,
		RetentionDuration: time.Duration(subSpec.MessageRetention),
	}
	if subSpec.AckDeadlineSeconds > 0 {
		gcpConfig.AckDeadline = time.Duration(subSpec.AckDeadlineSeconds) * time.Second
	}
	if subSpec.RetryPolicy != nil {
		gcpConfig.RetryPolicy = &pubsub.RetryPolicy{
			MinimumBackoff: time.Duration(subSpec.RetryPolicy.MinimumBackoff), // Ensure correct type conversion
			MaximumBackoff: time.Duration(subSpec.RetryPolicy.MaximumBackoff), // Ensure correct type conversion
		}
	}

	s, err := a.client.CreateSubscription(ctx, subSpec.Name, gcpConfig)
	if err != nil {
		return nil, err
	}
	return &gcpSubscriptionAdapter{sub: s}, nil
}

func (a *gcpMessagingClientAdapter) Close() error { return a.client.Close() }

// Validate checks the resource configuration against Google Pub/Sub specific rules.
func (a *gcpMessagingClientAdapter) Validate(resources CloudResourcesSpec) error {
	for _, sub := range resources.Subscriptions {
		// If AckDeadlineSeconds is set, it must be within the allowed range.
		if sub.AckDeadlineSeconds != 0 {
			ackDuration := time.Duration(sub.AckDeadlineSeconds) * time.Second
			if ackDuration < minAckDeadline || ackDuration > maxAckDeadline {
				return fmt.Errorf("subscription '%s' has an invalid ack_deadline_seconds: %d. Must be between %d and %d",
					sub.Name, sub.AckDeadlineSeconds, int(minAckDeadline.Seconds()), int(maxAckDeadline.Seconds()))
			}
		}

		// If MessageRetention is set, it must be within the allowed range.
		if sub.MessageRetention != 0 {
			retentionDuration := time.Duration(sub.MessageRetention)
			if retentionDuration < minRetention || retentionDuration > maxRetention {
				return fmt.Errorf("subscription '%s' has an invalid message_retention: '%s'. Must be between '%s' and '%s'",
					sub.Name, retentionDuration, minRetention, maxRetention)
			}
		}
	}

	// Add any topic validation rules here if needed in the future.

	return nil
}

// Config fetches the current configuration of the subscription.
func (a *gcpSubscriptionAdapter) Config(ctx context.Context) (*SubscriptionConfig, error) {
	gcpConfig, err := a.sub.Config(ctx)
	if err != nil {
		return nil, err
	}
	return fromGCPSubscriptionConfig(&gcpConfig), nil
}

// CreateGoogleMessagingClient creates a real Pub/Sub client for use in production.
func CreateGoogleMessagingClient(ctx context.Context, projectID string, clientOpts ...option.ClientOption) (MessagingClient, error) {
	realClient, err := pubsub.NewClient(ctx, projectID, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("pubsub.NewClient: %w", err)
	}
	return MessagingClientFromPubsubClient(realClient), nil
}

// MessagingClientFromPubsubClient wraps a concrete *pubsub.Client to satisfy the MessagingClient interface.
func MessagingClientFromPubsubClient(client *pubsub.Client) MessagingClient {
	if client == nil {
		return nil
	}
	return &gcpMessagingClientAdapter{client: client}
}
