package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
)

// MessagingManager handles the creation, validation, and deletion of Pub/Sub topics and subscriptions.
// It orchestrates these operations through a generic MessagingClient interface.
type MessagingManager struct {
	client      MessagingClient
	logger      zerolog.Logger
	environment Environment
}

// NewMessagingManager creates a new manager for orchestrating Pub/Sub resources.
func NewMessagingManager(client MessagingClient, logger zerolog.Logger, environment Environment) (*MessagingManager, error) {
	if client == nil {
		return nil, errors.New("messaging client (MessagingClient interface) cannot be nil")
	}
	return &MessagingManager{
		client:      client,
		logger:      logger.With().Str("subcomponent", "MessagingManager").Logger(),
		environment: environment,
	}, nil
}

// CreateResources creates all configured Pub/Sub topics and subscriptions concurrently.
// It is idempotent; if a resource already exists, it will be skipped or updated as appropriate.
// It creates all topics first, then creates subscriptions that depend on them.
func (m *MessagingManager) CreateResources(ctx context.Context, resources CloudResourcesSpec) ([]ProvisionedTopic, []ProvisionedSubscription, error) {
	m.logger.Info().Msg("Starting Pub/Sub setup...")

	validationErr := m.client.Validate(resources)
	if validationErr != nil {
		m.logger.Error().Err(validationErr).Msg("Resource configuration failed validation")
		return nil, nil, validationErr
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
			log := m.logger.With().Str("topic", spec.Name).Logger()
			if spec.Name == "" {
				log.Warn().Msg("Skipping creation for topic with empty name")
				return
			}
			topic := m.client.Topic(spec.Name)
			exists, err := topic.Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check existence of topic '%s': %w", spec.Name, err)
				return
			}
			if exists {
				log.Info().Msg("Topic already exists, skipping creation.")
				provTopicChan <- ProvisionedTopic{Name: spec.Name}
				return
			}
			log.Info().Msg("Creating topic...")
			_, createErr := m.client.CreateTopicWithConfig(ctx, spec)
			if createErr != nil {
				errChan <- fmt.Errorf("failed to create topic '%s': %w", spec.Name, createErr)
			} else {
				log.Info().Msg("Topic created successfully.")
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
			log := m.logger.With().Str("subscription", spec.Name).Str("topic", spec.Topic).Logger()
			if spec.Name == "" || spec.Topic == "" {
				log.Warn().Msg("Skipping creation for subscription with empty name or topic")
				return
			}

			// This check is crucial: ensure the topic for the subscription exists before proceeding.
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
				log.Info().Msg("Subscription already exists, attempting to update.")
				_, updateErr := sub.Update(ctx, spec)
				if updateErr != nil {
					errChan <- fmt.Errorf("failed to update subscription '%s': %w", spec.Name, updateErr)
				} else {
					log.Info().Msg("Subscription updated successfully.")
					provSubChan <- ProvisionedSubscription{Name: spec.Name, Topic: spec.Topic}
				}
			} else {
				log.Info().Msg("Creating subscription...")
				_, createErr := m.client.CreateSubscription(ctx, spec)
				if createErr != nil {
					errChan <- fmt.Errorf("failed to create subscription '%s': %w", spec.Name, createErr)
				} else {
					log.Info().Msg("Subscription created successfully.")
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

// Teardown deletes all specified Pub/Sub resources concurrently. It deletes subscriptions
// first, then topics, to respect dependencies.
func (m *MessagingManager) Teardown(ctx context.Context, resources CloudResourcesSpec) error {
	m.logger.Info().Msg("Starting Pub/Sub teardown...")
	var allErrors []error

	// Teardown subscriptions first, as they depend on topics.
	subErr := m.teardownSubscriptions(ctx, resources.Subscriptions)
	if subErr != nil {
		allErrors = append(allErrors, subErr)
	}

	// Then teardown topics.
	topicErr := m.teardownTopics(ctx, resources.Topics)
	if topicErr != nil {
		allErrors = append(allErrors, topicErr)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("Pub/Sub teardown completed with errors: %w", errors.Join(allErrors...))
	}

	m.logger.Info().Msg("Pub/Sub teardown completed successfully.")
	return nil
}

// teardownTopics handles the concurrent deletion of topics.
func (m *MessagingManager) teardownTopics(ctx context.Context, topicsToTeardown []TopicConfig) error {
	m.logger.Info().Int("count", len(topicsToTeardown)).Msg("Tearing down Pub/Sub topics...")
	var wg sync.WaitGroup
	errChan := make(chan error, len(topicsToTeardown))

	for _, topicSpec := range topicsToTeardown {
		wg.Add(1)
		go func(spec TopicConfig) {
			defer wg.Done()
			log := m.logger.With().Str("topic", spec.Name).Logger()
			if spec.TeardownProtection {
				log.Warn().Msg("Teardown protection enabled, skipping deletion.")
				return
			}
			if spec.Name == "" {
				return
			}
			log.Info().Msg("Attempting to delete topic...")
			err := m.client.Topic(spec.Name).Delete(ctx)
			// It's not an error if the resource is already gone.
			if err != nil && !isNotFound(err) {
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

// teardownSubscriptions handles the concurrent deletion of subscriptions.
func (m *MessagingManager) teardownSubscriptions(ctx context.Context, subsToTeardown []SubscriptionConfig) error {
	m.logger.Info().Int("count", len(subsToTeardown)).Msg("Tearing down Pub/Sub subscriptions...")
	var wg sync.WaitGroup
	errChan := make(chan error, len(subsToTeardown))

	for _, subSpec := range subsToTeardown {
		wg.Add(1)
		go func(spec SubscriptionConfig) {
			defer wg.Done()
			log := m.logger.With().Str("subscription", spec.Name).Logger()
			if spec.TeardownProtection {
				log.Warn().Msg("Teardown protection enabled, skipping deletion.")
				return
			}
			if spec.Name == "" {
				return
			}
			log.Info().Msg("Attempting to delete subscription...")
			err := m.client.Subscription(spec.Name).Delete(ctx)
			// It's not an error if the resource is already gone.
			if err != nil && !isNotFound(err) {
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

// Verify checks if all specified Pub/Sub topics and subscriptions exist.
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
