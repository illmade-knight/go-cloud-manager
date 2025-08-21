package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
)

type Command struct {
	Instruction CommandInstruction `json:"instruction"`
	Payload     json.RawMessage    `json:"payload"`
}

// FoundationalSetupPayload is the payload for the initial 'dataflow-setup' command.
type FoundationalSetupPayload struct {
	DataflowName string `json:"dataflow_name"`
}
type CommandInstruction string

const (
	Setup    CommandInstruction = "dataflow-setup"
	Teardown CommandInstruction = "teardown"
	// REFACTOR: Added a new, explicit command for dependent resource setup.
	SetupDependent CommandInstruction = "dependent-resource-setup"
)

// DependentSetupPayload contains the dataflow name and the map of deployed service URLs.
type DependentSetupPayload struct {
	DataflowName string            `json:"dataflow_name"`
	ServiceURLs  map[string]string `json:"service_urls"`
}

type CompletionEvent struct {
	Status       CompletionStatus             `json:"status"`
	Value        string                       `json:"value"`
	ErrorMessage string                       `json:"error_message,omitempty"`
	AppliedIAM   map[string]iam.PolicyBinding `json:"applied_iam,omitempty"`
}
type CompletionStatus string

const (
	ServiceDirectorReady CompletionStatus = "setup_complete"
	DataflowComplete     CompletionStatus = "dataflow_complete"
)

const ServiceDirector = "service-director"

// RemoteDirectorClient manages all asynchronous communication with the remote ServiceDirector.
type RemoteDirectorClient struct {
	arch                   *servicemanager.MicroserviceArchitecture
	logger                 zerolog.Logger
	psClient               *pubsub.Client
	cancelFunc             context.CancelFunc
	commandTopic           *pubsub.Topic
	completionSubscription *pubsub.Subscription
	waiters                map[string]chan CompletionEvent
	waitersMutex           sync.Mutex
}

// NewRemoteDirectorClient creates a new client for communicating with the ServiceDirector.
func NewRemoteDirectorClient(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, clientOpts ...option.ClientOption) (*RemoteDirectorClient, error) {
	psClient, err := pubsub.NewClient(ctx, arch.ProjectID, clientOpts...)
	if err != nil {
		return nil, err
	}

	internalCtx, cancel := context.WithCancel(ctx)
	client := &RemoteDirectorClient{
		arch:       arch,
		logger:     logger.With().Str("component", "RemoteDirectorClient").Logger(),
		psClient:   psClient,
		cancelFunc: cancel,
		waiters:    make(map[string]chan CompletionEvent),
	}

	if err := client.initialize(internalCtx); err != nil {
		return nil, err
	}
	return client, nil
}

// initialize sets up the Pub/Sub topics and subscriptions needed for communication.
func (c *RemoteDirectorClient) initialize(ctx context.Context) error {
	c.logger.Info().Msg("Initializing remote director client command infrastructure...")

	// REFACTOR: This now correctly resolves the topic names by searching the architecture,
	// using the ServiceMapping reference as the key. This removes the flawed dependency
	// on environment variables and respects the single source of truth principle.
	commandTopicID, err := findTopicNameByMapping(c.arch, c.arch.ServiceManagerSpec.CommandTopic)
	if err != nil {
		return fmt.Errorf("could not resolve command topic: %w", err)
	}

	completionTopicID, err := findTopicNameByMapping(c.arch, c.arch.ServiceManagerSpec.CompletionTopic)
	if err != nil {
		return fmt.Errorf("could not resolve completion topic: %w", err)
	}

	c.logger.Info().Str("command_topic", commandTopicID).Str("completion_topic", completionTopicID).Msg("Resolved ServiceDirector topics.")

	commandTopic, err := ensureTopicExists(ctx, c.psClient, commandTopicID)
	if err != nil {
		return err
	}
	c.commandTopic = commandTopic

	completionTopic, err := ensureTopicExists(ctx, c.psClient, completionTopicID)
	if err != nil {
		return err
	}

	subID := fmt.Sprintf("conductor-listener-%s", uuid.New().String()[:8])
	sub, err := c.psClient.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic:       completionTopic,
		AckDeadline: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("failed to create completion subscription: %w", err)
	}
	c.completionSubscription = sub

	go c.listenForEvents(ctx)
	c.logger.Info().Msg("Remote director client initialized and listening for events.")
	return nil
}

// findTopicNameByMapping is a helper that searches the entire architecture for a topic
// resource that matches the (hydrated) name in a ServiceMapping reference.
func findTopicNameByMapping(arch *servicemanager.MicroserviceArchitecture, mapping servicemanager.ServiceMapping) (string, error) {
	hydratedName := mapping.Name
	for _, df := range arch.Dataflows {
		for _, topic := range df.Resources.Topics {
			if topic.Name == hydratedName {
				return topic.Name, nil
			}
		}
	}
	return "", fmt.Errorf("topic resource referenced by name '%s' not found in any dataflow", hydratedName)
}

// listenForEvents is the core listener that routes incoming events to the correct waiting goroutine.
func (c *RemoteDirectorClient) listenForEvents(ctx context.Context) {
	err := c.completionSubscription.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		var event CompletionEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			c.logger.Error().Err(err).Msg("Failed to unmarshal completion event")
			return
		}
		c.logger.Info().Str("status", string(event.Status)).Str("value", event.Value).Msg("Received event")

		c.waitersMutex.Lock()
		if ch, ok := c.waiters[event.Value]; ok {
			ch <- event
		} else {
			c.logger.Warn().Str("key", event.Value).Msg("Received event for which there was no active waiter.")
		}
		c.waitersMutex.Unlock()
		msg.Ack()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Error().Err(err).Msg("Remote director client event listener shut down with error")
	}
}

// TriggerFoundationalSetup sends the command to set up foundational resources and waits for completion.
func (c *RemoteDirectorClient) TriggerFoundationalSetup(ctx context.Context, dataflowName string) (CompletionEvent, error) {
	payload := FoundationalSetupPayload{DataflowName: dataflowName}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return CompletionEvent{}, fmt.Errorf("failed to marshal foundational setup payload: %w", err)
	}
	cmd := Command{Instruction: Setup, Payload: payloadBytes}
	return c.sendCommandAndWait(ctx, dataflowName, cmd)
}

// TriggerDependentSetup sends the command to set up dependent resources and waits for completion.
func (c *RemoteDirectorClient) TriggerDependentSetup(ctx context.Context, dataflowName string, serviceURLs map[string]string) (CompletionEvent, error) {
	payload := DependentSetupPayload{
		DataflowName: dataflowName,
		ServiceURLs:  serviceURLs,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return CompletionEvent{}, fmt.Errorf("failed to marshal dependent setup payload: %w", err)
	}
	cmd := Command{Instruction: SetupDependent, Payload: payloadBytes}
	waiterKey := dataflowName + "-dependent"
	return c.sendCommandAndWait(ctx, waiterKey, cmd)
}

// sendCommandAndWait is the generic, blocking helper that manages the async request/reply.
func (c *RemoteDirectorClient) sendCommandAndWait(ctx context.Context, waiterKey string, cmd Command) (CompletionEvent, error) {
	waitChan := make(chan CompletionEvent, 1)
	c.waitersMutex.Lock()
	c.waiters[waiterKey] = waitChan
	c.waitersMutex.Unlock()

	defer func() {
		c.waitersMutex.Lock()
		delete(c.waiters, waiterKey)
		c.waitersMutex.Unlock()
	}()

	cmdMsg, err := json.Marshal(cmd)
	if err != nil {
		return CompletionEvent{}, fmt.Errorf("failed to marshal command: %w", err)
	}
	result := c.commandTopic.Publish(ctx, &pubsub.Message{Data: cmdMsg})
	_, err = result.Get(ctx)
	if err != nil {
		return CompletionEvent{}, fmt.Errorf("failed to publish command: %w", err)
	}

	select {
	case event := <-waitChan:
		if event.Status == "failure" {
			return event, fmt.Errorf("remote operation for '%s' failed: %s", waiterKey, event.ErrorMessage)
		}
		return event, nil
	case <-time.After(10 * time.Minute):
		return CompletionEvent{}, fmt.Errorf("timeout waiting for completion event for '%s'", waiterKey)
	case <-ctx.Done():
		return CompletionEvent{}, ctx.Err()
	}
}

// Teardown cleans up the client's Pub/Sub resources.
func (c *RemoteDirectorClient) Teardown(ctx context.Context) error {
	c.cancelFunc()
	var allErrors []string
	if c.completionSubscription != nil {
		if err := c.completionSubscription.Delete(ctx); err != nil {
			allErrors = append(allErrors, fmt.Sprintf("failed to delete completion subscription: %v", err))
		}
	}
	if c.psClient != nil {
		_ = c.psClient.Close()
	}
	if len(allErrors) > 0 {
		return fmt.Errorf("remote client cleanup encountered errors: %s", strings.Join(allErrors, "; "))
	}
	return nil
}

func ensureTopicExists(ctx context.Context, client *pubsub.Client, topicID string) (*pubsub.Topic, error) {
	topic := client.Topic(topicID)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check for topic '%s': %w", topicID, err)
	}
	if exists {
		return topic, nil
	}
	return client.CreateTopic(ctx, topicID)
}
