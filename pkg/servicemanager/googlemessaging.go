package servicemanager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const (
	// Google Cloud Pub/Sub constraints define the valid ranges for certain settings.
	minAckDeadline = 10 * time.Second
	maxAckDeadline = 600 * time.Second
	minRetention   = 10 * time.Minute
	maxRetention   = 7 * 24 * time.Hour
)

// --- Conversion Helpers ---

// fromGCPTopic converts a Google Pub/Sub topic protobuf
// into the generic TopicConfig used by the ServiceManager.
func fromGCPTopic(t *pubsubpb.Topic) *TopicConfig {
	// The name from the protobuf is the fully qualified name. We only want the ID.
	parts := strings.Split(t.Name, "/")
	name := parts[len(parts)-1]
	return &TopicConfig{
		CloudResource: CloudResource{
			Name:   name,
			Labels: t.Labels,
		},
	}
}

// fromGCPSubscription converts a Google Pub/Sub subscription protobuf
// into the generic SubscriptionConfig used by the ServiceManager.
func fromGCPSubscription(s *pubsubpb.Subscription) *SubscriptionConfig {
	// The names from the protobuf are fully qualified. We only want the IDs.
	nameParts := strings.Split(s.Name, "/")
	name := nameParts[len(nameParts)-1]
	topicParts := strings.Split(s.Topic, "/")
	topic := topicParts[len(topicParts)-1]

	spec := &SubscriptionConfig{
		CloudResource: CloudResource{
			Name:   name,
			Labels: s.Labels,
		},
		Topic:              topic,
		AckDeadlineSeconds: int(s.AckDeadlineSeconds),
	}

	if s.MessageRetentionDuration != nil {
		spec.MessageRetention = Duration(s.MessageRetentionDuration.AsDuration())
	}

	if s.RetryPolicy != nil {
		spec.RetryPolicy = &RetryPolicySpec{
			MinimumBackoff: Duration(s.RetryPolicy.MinimumBackoff.AsDuration()),
			MaximumBackoff: Duration(s.RetryPolicy.MaximumBackoff.AsDuration()),
		}
	}

	return spec
}

// --- Adapter Implementations ---

// gcpTopicAdapter wraps a v2 pubsub.Client to satisfy the MessagingTopic interface for admin tasks.
type gcpTopicAdapter struct {
	client *pubsub.Client
	name   string // Fully qualified name
}

func (a *gcpTopicAdapter) ID() string {
	parts := strings.Split(a.name, "/")
	return parts[len(parts)-1]
}

func (a *gcpTopicAdapter) Exists(ctx context.Context) (bool, error) {
	_, err := a.client.TopicAdminClient.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: a.name})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *gcpTopicAdapter) Delete(ctx context.Context) error {
	return a.client.TopicAdminClient.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{Topic: a.name})
}

func (a *gcpTopicAdapter) Update(ctx context.Context, cfg TopicConfig) (*TopicConfig, error) {
	return nil, fmt.Errorf("update not implemented for v2 topic adapter")
}

// gcpSubscriptionAdapter wraps a v2 pubsub.Client to satisfy the MessagingSubscription interface for admin tasks.
type gcpSubscriptionAdapter struct {
	client *pubsub.Client
	name   string // Fully qualified name
}

func (a *gcpSubscriptionAdapter) ID() string {
	parts := strings.Split(a.name, "/")
	return parts[len(parts)-1]
}

func (a *gcpSubscriptionAdapter) Exists(ctx context.Context) (bool, error) {
	_, err := a.client.SubscriptionAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: a.name})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (a *gcpSubscriptionAdapter) Delete(ctx context.Context) error {
	return a.client.SubscriptionAdminClient.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: a.name})
}

func (a *gcpSubscriptionAdapter) Update(ctx context.Context, spec SubscriptionConfig) (*SubscriptionConfig, error) {
	currentPbSub, err := a.client.SubscriptionAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: a.name})
	if err != nil {
		return nil, fmt.Errorf("failed to get current config for subscription '%s': %w", spec.Name, err)
	}

	updateReq := &pubsubpb.UpdateSubscriptionRequest{
		Subscription: &pubsubpb.Subscription{Name: a.name},
		UpdateMask:   &fieldmaskpb.FieldMask{},
	}
	needsUpdate := false

	if spec.AckDeadlineSeconds > 0 && int32(spec.AckDeadlineSeconds) != currentPbSub.AckDeadlineSeconds {
		updateReq.Subscription.AckDeadlineSeconds = int32(spec.AckDeadlineSeconds)
		updateReq.UpdateMask.Paths = append(updateReq.UpdateMask.Paths, "ack_deadline_seconds")
		needsUpdate = true
	}

	if spec.MessageRetention > 0 && time.Duration(spec.MessageRetention) != currentPbSub.MessageRetentionDuration.AsDuration() {
		updateReq.Subscription.MessageRetentionDuration = durationpb.New(time.Duration(spec.MessageRetention))
		updateReq.UpdateMask.Paths = append(updateReq.UpdateMask.Paths, "message_retention_duration")
		needsUpdate = true
	}

	if !needsUpdate {
		return fromGCPSubscription(currentPbSub), nil
	}

	updatedPbSub, err := a.client.SubscriptionAdminClient.UpdateSubscription(ctx, updateReq)
	if err != nil {
		return nil, fmt.Errorf("failed to apply update to subscription '%s': %w", spec.Name, err)
	}
	return fromGCPSubscription(updatedPbSub), nil
}

// gcpMessagingClientAdapter wraps a v2 pubsub.Client to satisfy the MessagingClient interface.
type gcpMessagingClientAdapter struct {
	client    *pubsub.Client
	projectID string
}

func (a *gcpMessagingClientAdapter) Topic(id string) MessagingTopic {
	fqn := fmt.Sprintf("projects/%s/topics/%s", a.projectID, id)
	return &gcpTopicAdapter{client: a.client, name: fqn}
}

func (a *gcpMessagingClientAdapter) Subscription(id string) MessagingSubscription {
	fqn := fmt.Sprintf("projects/%s/subscriptions/%s", a.projectID, id)
	return &gcpSubscriptionAdapter{client: a.client, name: fqn}
}

func (a *gcpMessagingClientAdapter) CreateTopic(ctx context.Context, topicID string) (MessagingTopic, error) {
	return a.CreateTopicWithConfig(ctx, TopicConfig{CloudResource: CloudResource{Name: topicID}})
}

func (a *gcpMessagingClientAdapter) CreateTopicWithConfig(ctx context.Context, topicSpec TopicConfig) (MessagingTopic, error) {
	topicName := fmt.Sprintf("projects/%s/topics/%s", a.projectID, topicSpec.Name)
	_, err := a.client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{
		Name:   topicName,
		Labels: topicSpec.Labels,
	})
	if err != nil {
		return nil, err
	}
	return a.Topic(topicSpec.Name), nil
}

func (a *gcpMessagingClientAdapter) CreateSubscription(ctx context.Context, subSpec SubscriptionConfig) (MessagingSubscription, error) {
	topicName := fmt.Sprintf("projects/%s/topics/%s", a.projectID, subSpec.Topic)
	subName := fmt.Sprintf("projects/%s/subscriptions/%s", a.projectID, subSpec.Name)

	gcpConfig := &pubsubpb.Subscription{
		Name:               subName,
		Topic:              topicName,
		Labels:             subSpec.Labels,
		AckDeadlineSeconds: int32(subSpec.AckDeadlineSeconds),
	}

	if subSpec.MessageRetention > 0 {
		gcpConfig.MessageRetentionDuration = durationpb.New(time.Duration(subSpec.MessageRetention))
	}

	if subSpec.RetryPolicy != nil {
		gcpConfig.RetryPolicy = &pubsubpb.RetryPolicy{
			MinimumBackoff: durationpb.New(time.Duration(subSpec.RetryPolicy.MinimumBackoff)),
			MaximumBackoff: durationpb.New(time.Duration(subSpec.RetryPolicy.MaximumBackoff)),
		}
	}

	_, err := a.client.SubscriptionAdminClient.CreateSubscription(ctx, gcpConfig)
	if err != nil {
		return nil, err
	}
	return a.Subscription(subSpec.Name), nil
}

func (a *gcpMessagingClientAdapter) Close() error {
	return a.client.Close()
}

func (a *gcpMessagingClientAdapter) Validate(resources CloudResourcesSpec) error {
	for _, sub := range resources.Subscriptions {
		if sub.AckDeadlineSeconds != 0 {
			ackDuration := time.Duration(sub.AckDeadlineSeconds) * time.Second
			if ackDuration < minAckDeadline || ackDuration > maxAckDeadline {
				return fmt.Errorf("subscription '%s' has an invalid ack_deadline_seconds: %d. Must be between %d and %d",
					sub.Name, sub.AckDeadlineSeconds, int(minAckDeadline.Seconds()), int(maxAckDeadline.Seconds()))
			}
		}

		if sub.MessageRetention != 0 {
			retentionDuration := time.Duration(sub.MessageRetention)
			if retentionDuration < minRetention || retentionDuration > maxRetention {
				return fmt.Errorf("subscription '%s' has an invalid message_retention: '%s'. Must be between '%s' and '%s'",
					sub.Name, retentionDuration, minRetention, maxRetention)
			}
		}
	}
	return nil
}

// --- Factory Functions ---

// CreateGoogleMessagingClient creates a v2 client wrapped in the MessagingClient interface.
func CreateGoogleMessagingClient(ctx context.Context, projectID string, clientOpts ...option.ClientOption) (MessagingClient, error) {
	client, err := pubsub.NewClient(ctx, projectID, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("pubsub.NewClient: %w", err)
	}

	return &gcpMessagingClientAdapter{
		client:    client,
		projectID: projectID,
	}, nil
}
