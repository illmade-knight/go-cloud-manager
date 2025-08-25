package main

import (
	"context"
	_ "embed" // REFACTOR: Import the embed package
	"fmt"
	"os"

	"github.com/illmade-knight/go-cloud-manager/microservice/servicedirector"
	"github.com/illmade-knight/go-cloud-manager/pkg/orchestration"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

//go:embed resources.yaml
var resourcesYAML []byte

// REFACTOR: This directive tells the Go compiler to embed the contents of
// services.yaml into the servicesYAML byte slice at build time.
//
//go:embed services.yaml
var servicesYAML []byte

// TODO on next push use the readResourcesYAML method in microservice/servicedirector instead
func readResourcesYAML() (*servicedirector.PubsubConfig, error) {
	spec := &servicemanager.CloudResourcesSpec{}
	err := yaml.Unmarshal(resourcesYAML, spec)
	if err != nil {
		// Changed from Fatalf to return an error for better handling
		return nil, fmt.Errorf("failed to parse embedded resources.yaml: %w", err)
	}

	lookupMap := orchestration.ReadResourceMappings(spec)

	cfg := &servicedirector.PubsubConfig{}
	var ok bool

	// Get subscription name from the map
	cfg.CommandSubID, ok = lookupMap["command-subscription-id"]
	if !ok {
		return nil, fmt.Errorf("failed to find command-subscription-id in resources.yaml")
	}

	// Find the subscription in the spec to get its topic
	var commandTopicName string
	for _, sub := range spec.Subscriptions {
		if sub.Name == cfg.CommandSubID {
			commandTopicName = sub.Topic
			break
		}
	}
	if commandTopicName == "" {
		return nil, fmt.Errorf("could not find topic for subscription %s", cfg.CommandSubID)
	}
	cfg.CommandTopicID = commandTopicName

	// Get completion topic name from the map
	cfg.CompletionTopicID, ok = lookupMap["completion-topic-id"]
	if !ok {
		return nil, fmt.Errorf("failed to find completion-topic-id in resources.yaml")
	}

	return cfg, nil
}

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("component", "servicedirector-service").Logger()
	ctx := context.Background()

	// REFACTOR: Load the architecture from the embedded byte slice, not a file path.
	// 3. Load and Prepare the Architecture Definition
	arch := &servicemanager.MicroserviceArchitecture{}
	if err := yaml.Unmarshal(servicesYAML, arch); err != nil {
		logger.Fatal().Err(err).Msg("Failed to parse embedded services.yaml")
	}

	cfg, err := servicedirector.NewConfig()
	if err != nil {
		logger.Fatal().Err(err).Msg("Configuration error")
	}

	pubsub, err := readResourcesYAML()
	if err == nil {
		logger.Info().Msg("loaded resources.yaml")
		cfg.Commands = pubsub
	} else {
		logger.Err(err).Msg("error loading yaml")
	}

	sd, err := servicedirector.NewServiceDirector(ctx, cfg, arch, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize ServiceDirector")
	}

	if err := sd.Start(); err != nil {
		logger.Fatal().Err(err).Msg("Service terminated with an error")
	}
}
