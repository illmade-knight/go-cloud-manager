package orchestration

import (
	"cloud.google.com/go/pubsub"
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
	"time"
)

// WaitForState is a helper to listen on the state channel for a target state.
func WaitForState(ctx context.Context, orch *Orchestrator, targetState OrchestratorState) error {
	for {
		select {
		case state, ok := <-orch.StateChan():
			if !ok {
				return fmt.Errorf("orchestrator state channel closed prematurely")
			}
			log.Info().Str("state", string(state)).Msg("Orchestrator state changed")
			if state == targetState {
				return nil // Success!
			}
			if state == StateError {
				return fmt.Errorf("orchestrator entered an error state")
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
			return fmt.Errorf("timed out waiting for orchestrator state: %s", targetState)
		}
	}
}

// ensureTopicExists is a private helper to create a Pub/Sub topic if it doesn't already exist.
func ensureTopicExists(ctx context.Context, client *pubsub.Client, topicID string) error {
	topic := client.Topic(topicID)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for topic %s: %w", topicID, err)
	}
	if !exists {
		log.Info().Str("topic", topicID).Msg("Topic not found, creating it now.")
		_, err = client.CreateTopic(ctx, topicID)
		if err != nil {
			return fmt.Errorf("failed to create topic %s: %w", topicID, err)
		}
	}
	return nil
}
