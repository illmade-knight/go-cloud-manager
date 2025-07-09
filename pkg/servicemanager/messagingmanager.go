package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"sync"
)

// --- PubSub Manager ---

// MessagingManager handles the creation and deletion of Pub/Sub topics and subscriptions.
type MessagingManager struct {
	client      MessagingClient
	logger      zerolog.Logger
	environment Environment
}

// NewMessagingManager creates a new MessagingManager.
func NewMessagingManager(client MessagingClient, logger zerolog.Logger, environment Environment) (*MessagingManager, error) {
	if client == nil {
		return nil, fmt.Errorf("PubSub client (MessagingClient interface) cannot be nil")
	}
	return &MessagingManager{
		client:      client,
		logger:      logger.With().Str("subcomponent", "MessagingManager").Logger(),
		environment: environment,
	}, nil
}

// CreateResources creates all configured Pub/Sub topics and subscriptions concurrently.
func (m *MessagingManager) CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedTopic, []ProvisionedSubscription, error) {
	m.logger.Info().Msg("Starting Pub/Sub setup...")

	if err := m.client.Validate(resources); err != nil {
		m.logger.Error().Err(err).Msg("Resource configuration failed validation")
		return nil, nil, err
	}
	m.logger.Info().Msg("Resource configuration is valid")

	var allErrors []error
	var provisionedTopics []ProvisionedTopic
	var provisionedSubscriptions []ProvisionedSubscription
	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.Topics)+len(resources.Subscriptions))

	// ---- 1. Create Topics Concurrently ----
	provTopicChan := make(chan ProvisionedTopic, len(resources.Topics))
	m.logger.Info().Int("count", len(resources.Topics)).Msg("Processing topics...")
	for _, topicSpec := range resources.Topics {
		wg.Add(1)
		go func(spec TopicConfig) {
			defer wg.Done()
			if spec.Name == "" {
				m.logger.Warn().Msg("Skipping creation for topic with empty name")
				return
			}
			topic := m.client.Topic(spec.Name)
			exists, err := topic.Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check existence of topic '%s': %w", spec.Name, err)
				return
			}
			if exists {
				m.logger.Info().Str("topic_id", spec.Name).Msg("Topic already exists, skipping creation.")
				provTopicChan <- ProvisionedTopic{Name: spec.Name}
				return
			}
			m.logger.Info().Str("topic_id", spec.Name).Msg("Creating topic...")
			if _, err := m.client.CreateTopicWithConfig(ctx, spec); err != nil {
				errChan <- fmt.Errorf("failed to create topic '%s': %w", spec.Name, err)
			} else {
				m.logger.Info().Str("topic_id", spec.Name).Msg("Topic created successfully.")
				provTopicChan <- ProvisionedTopic{Name: spec.Name}
			}
		}(topicSpec)
	}
	wg.Wait()
	close(provTopicChan)
	for pt := range provTopicChan {
		provisionedTopics = append(provisionedTopics, pt)
	}

	// ---- 2. Create Subscriptions Concurrently ----
	provSubChan := make(chan ProvisionedSubscription, len(resources.Subscriptions))
	m.logger.Info().Int("count", len(resources.Subscriptions)).Msg("Processing subscriptions...")
	for _, subSpec := range resources.Subscriptions {
		wg.Add(1)
		go func(spec SubscriptionConfig) {
			defer wg.Done()
			if spec.Name == "" || spec.Topic == "" {
				return
			}

			// This check is crucial and must remain in the manager
			topic := m.client.Topic(spec.Topic)
			topicExists, err := topic.Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check existence of topic '%s' for subscription '%s': %w", spec.Topic, spec.Name, err)
				return
			}
			if !topicExists {
				errChan <- fmt.Errorf("topic '%s' for subscription '%s' does not exist", spec.Topic, spec.Name)
				return
			}

			sub := m.client.Subscription(spec.Name)
			subExists, err := sub.Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check existence of subscription '%s': %w", spec.Name, err)
				return
			}

			if subExists {
				m.logger.Info().Str("sub_name", spec.Name).Msg("Subscription already exists, attempting to update.")
				if _, err := sub.Update(ctx, spec); err != nil {
					errChan <- fmt.Errorf("failed to update subscription '%s': %w", spec.Name, err)
				} else {
					m.logger.Info().Str("sub_name", spec.Name).Msg("Subscription updated successfully.")
					provSubChan <- ProvisionedSubscription{Name: spec.Name, Topic: spec.Topic}
				}
			} else {
				m.logger.Info().Str("sub_name", spec.Name).Msg("Creating subscription...")
				if _, err := m.client.CreateSubscription(ctx, spec); err != nil {
					errChan <- fmt.Errorf("failed to create subscription '%s': %w", spec.Name, err)
				} else {
					m.logger.Info().Str("sub_name", spec.Name).Msg("Subscription created successfully.")
					provSubChan <- ProvisionedSubscription{Name: spec.Name, Topic: spec.Topic}
				}
			}
		}(subSpec)
	}
	wg.Wait()
	close(provSubChan)
	close(errChan)

	for ps := range provSubChan {
		provisionedSubscriptions = append(provisionedSubscriptions, ps)
	}
	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return provisionedTopics, provisionedSubscriptions, errors.Join(allErrors...)
	}

	m.logger.Info().Msg("Pub/Sub setup completed successfully.")
	return provisionedTopics, provisionedSubscriptions, nil
}

// Teardown deletes Pub/Sub resources concurrently in proper dependency order.
func (m *MessagingManager) Teardown(ctx context.Context, resources CloudResourcesSpec) error {
	m.logger.Info().Msg("Starting Pub/Sub teardown...")
	var allErrors []error

	// Teardown subscriptions first
	if err := m.teardownSubscriptions(ctx, resources.Subscriptions); err != nil {
		allErrors = append(allErrors, err)
	}

	// Then teardown topics
	if err := m.teardownTopics(ctx, resources.Topics); err != nil {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("Pub/Sub teardown completed with errors: %w", errors.Join(allErrors...))
	}

	m.logger.Info().Msg("Pub/Sub teardown completed successfully.")
	return nil
}

func (m *MessagingManager) teardownTopics(ctx context.Context, topicsToTeardown []TopicConfig) error {
	m.logger.Info().Int("count", len(topicsToTeardown)).Msg("Tearing down Pub/Sub topics...")
	var wg sync.WaitGroup
	errChan := make(chan error, len(topicsToTeardown))

	for _, topicSpec := range topicsToTeardown {
		wg.Add(1)
		go func(spec TopicConfig) {
			defer wg.Done()
			if spec.TeardownProtection {
				m.logger.Warn().Str("name", spec.Name).Msg("teardown protection in place for topic")
				return
			}
			if spec.Name == "" {
				return
			}
			m.logger.Info().Str("topic_id", spec.Name).Msg("Attempting to delete topic...")
			if err := m.client.Topic(spec.Name).Delete(ctx); err != nil && !isNotFound(err) {
				errChan <- fmt.Errorf("failed to delete topic %s: %w", spec.Name, err)
			}
		}(topicSpec)
	}
	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}
	return errors.Join(allErrors...)
}

func (m *MessagingManager) teardownSubscriptions(ctx context.Context, subsToTeardown []SubscriptionConfig) error {
	m.logger.Info().Int("count", len(subsToTeardown)).Msg("Tearing down Pub/Sub subscriptions...")
	var wg sync.WaitGroup
	errChan := make(chan error, len(subsToTeardown))

	for _, subSpec := range subsToTeardown {
		wg.Add(1)
		go func(spec SubscriptionConfig) {
			defer wg.Done()
			if spec.TeardownProtection {
				m.logger.Warn().Str("name", spec.Name).Msg("teardown protection in place for subscription")
				return
			}
			if spec.Name == "" {
				return
			}
			m.logger.Info().Str("sub_name", spec.Name).Msg("Attempting to delete subscription...")
			if err := m.client.Subscription(spec.Name).Delete(ctx); err != nil && !isNotFound(err) {
				errChan <- fmt.Errorf("failed to delete subscription %s: %w", spec.Name, err)
			}
		}(subSpec)
	}
	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}
	return errors.Join(allErrors...)
}

// Verify checks all Pub/Sub resources concurrently.
func (m *MessagingManager) Verify(ctx context.Context, resources CloudResourcesSpec) error {
	m.logger.Info().Msg("Verifying Pub/Sub resources...")
	var allErrors []error
	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.Topics)+len(resources.Subscriptions))

	// Verify Topics
	for _, topicSpec := range resources.Topics {
		wg.Add(1)
		go func(spec TopicConfig) {
			defer wg.Done()
			exists, err := m.client.Topic(spec.Name).Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check topic '%s': %w", spec.Name, err)
			} else if !exists {
				errChan <- fmt.Errorf("topic '%s' not found", spec.Name)
			}
		}(topicSpec)
	}

	// Verify Subscriptions
	for _, subSpec := range resources.Subscriptions {
		wg.Add(1)
		go func(spec SubscriptionConfig) {
			defer wg.Done()
			exists, err := m.client.Subscription(spec.Name).Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check subscription '%s': %w", spec.Name, err)
			} else if !exists {
				errChan <- fmt.Errorf("subscription '%s' not found", spec.Name)
			}
		}(subSpec)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("Pub/Sub verification failed: %w", errors.Join(allErrors...))
	}

	m.logger.Info().Msg("Pub/Sub verification completed successfully.")
	return nil
}
