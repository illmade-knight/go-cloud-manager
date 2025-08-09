//go:build cloud_integration || integration

package orchestration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// --- Toy Application Source Code Definitions ---

// toyAppSource defines a minimal, dependency-free "hello world" application.
// It's used for basic deployment verification tests.
var toyAppSource = map[string]string{
	"go.mod": `
module toy-app-for-testing
go 1.22
`,
	"main.go": `
package main
import (
	"fmt"
	"log"
	"net/http"
	"os"
)
func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Toy App is Healthy!")
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("INFO: Toy App listening on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
`,
}

// serviceDirectorAppSource defines a more realistic, but still self-contained,
// version of the ServiceDirector application for testing purposes. It listens for a command
// on one Pub/Sub topic and publishes a reply on another, simulating the real application's behavior.
var serviceDirectorAppSource = map[string]string{
	"go.mod": `
module toy-servicedirector
go 1.22

require (
	cloud.google.com/go/pubsub v1.33.0
	github.com/rs/zerolog v1.26.1
)
`,
	"main.go": `
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/rs/zerolog"
)

// Command represents the structure of a message from the orchestrator.
type Command struct {
	Instruction  string 
	Value        string 
}

// CompletionEvent is the message published back to the orchestrator.
type CompletionEvent struct {
	Status       string 
	Value        string 
	ErrorMessage string
}

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctx := context.Background()

	projectID := os.Getenv("PROJECT_ID")
	commandTopicID := os.Getenv("SD_COMMAND_TOPIC")
	commandSubID := os.Getenv("SD_COMMAND_SUBSCRIPTION")
	completionTopicID := os.Getenv("SD_COMPLETION_TOPIC")

	if projectID == "" || commandTopicID == "" || commandSubID == "" || completionTopicID == "" {
		logger.Fatal().Msg("Missing required environment variables (PROJECT_ID, SD_COMMAND_TOPIC, etc.)")
	}

	psClient, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create Pub/Sub client")
	}
	defer psClient.Close()

	commandTopic := psClient.Topic(commandTopicID)
	sub := psClient.Subscription(commandSubID)
	exists, err := sub.Exists(ctx)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to check for subscription")
	}
	if !exists {
		logger.Info().Str("subscription", commandSubID).Msg("Creating command subscription...")
		_, err = psClient.CreateSubscription(ctx, commandSubID, pubsub.SubscriptionConfig{
			Topic:       commandTopic,
			AckDeadline: 20 * time.Second,
		})
		if err != nil {
			logger.Fatal().Err(err).Msg("Failed to create command subscription")
		}
	}

	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "OK") })
		port := os.Getenv("PORT"); if port == "" { port = "8080" }
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			logger.Warn().Err(err).Msg("Health check server failed")
		}
	}()

	logger.Info().Str("subscription", commandSubID).Msg("Toy ServiceDirector starting to listen for commands...")
	err = sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		logger.Info().Str("data", string(msg.Data)).Msg("Received command message")

		var cmd Command
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			logger.Error().Err(err).Msg("Failed to unmarshal command")
			return
		}

		time.Sleep(2 * time.Second)

		completionTopic := psClient.Topic(completionTopicID)
		event := CompletionEvent{Status: "setup_complete", Value: cmd.Value}
		eventData, _ := json.Marshal(event)

		result := completionTopic.Publish(ctx, &pubsub.Message{Data: eventData})
		id, err := result.Get(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to publish completion event")
			return
		}
		logger.Info().Str("message_id", id).Msg("Published completion event")
	})

	if err != nil {
		logger.Fatal().Err(err).Msg("Receive error")
	}
}
`,
}

// --- Test Helper Functions ---

// createTestSourceDir is a helper to write a map of source files to a temporary directory.
func createTestSourceDir(t *testing.T, files map[string]string) (string, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "source")
	require.NoError(t, err)

	for name, content := range files {
		p := filepath.Join(tmpDir, name)
		dir := filepath.Dir(p)
		err = os.MkdirAll(dir, 0755)
		require.NoError(t, err, "Failed to create source subdirectories")
		err = os.WriteFile(p, []byte(content), 0644)
		require.NoError(t, err, "Failed to write source file")
	}

	return tmpDir, func() {
		_ = os.RemoveAll(tmpDir)
	}
}

// createVerificationResources creates the topic and subscription used by the test to verify results.
// createVerificationResources creates the topic and subscription used by the test to verify results.
// MODIFIED: This function now takes the final, hydrated topic name directly.
func createVerificationResources(t *testing.T, ctx context.Context, client *pubsub.Client, hydratedTopicName string) (*pubsub.Topic, *pubsub.Subscription) {
	t.Helper()
	// The topic is created using the exact name provided by the hydrated architecture.
	verifyTopic := client.Topic(hydratedTopicName)
	exists, err := verifyTopic.Exists(ctx)
	require.NoError(t, err)
	require.True(t, exists)
	require.NotNil(t, verifyTopic)

	// The subscription is ephemeral to the test runner, so its name can be unique.
	subID := fmt.Sprintf("verify-sub-%s", uuid.New().String()[:8])
	verifySub, err := client.CreateSubscription(ctx, subID, pubsub.SubscriptionConfig{
		Topic:               verifyTopic,
		AckDeadline:         20 * time.Second,
		RetainAckedMessages: true,
		RetentionDuration:   10 * time.Minute,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		t.Log("Cleaning up verification resources...")
		cleanupCtx := context.Background()
		if err := verifySub.Delete(cleanupCtx); err != nil {
			t.Logf("Error deleting verify sub: %v", err)
		}
		if err := verifyTopic.Delete(cleanupCtx); err != nil {
			t.Logf("Error deleting verify topic: %v", err)
		}
	})

	return verifyTopic, verifySub
}
