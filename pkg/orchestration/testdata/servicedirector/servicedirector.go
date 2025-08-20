package main

import (
	"context"
	_ "embed" // REFACTOR: Import the embed package
	"fmt"
	"log"
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

func readResourcesYAML() (*servicedirector.PubsubConfig, error) {
	spec := &servicemanager.CloudResourcesSpec{}
	err := yaml.Unmarshal(resourcesYAML, spec)
	if err != nil {
		log.Fatalf("Failed to parse services.yaml: %v", err)
	}

	lookupMap := orchestration.ReadResourceMappings(spec)

	cfg := &servicedirector.PubsubConfig{}
	v, ok := lookupMap["command-topic-id"]
	if ok {
		cfg.CommandTopicID = v
	} else {
		return nil, fmt.Errorf("failed to find commands-topic-id in resources.yaml")
	}
	v, ok = lookupMap["completion-topic-id"]
	if ok {
		cfg.CompletionTopicID = v
	} else {
		return nil, fmt.Errorf("failed to find events-topic-id in resources.yaml")
	}
	v, ok = lookupMap["command-subscription-id"]
	if ok {
		cfg.CommandSubID = v
	} else {
		return nil, fmt.Errorf("failed to find commands-sub-id in resources.yaml")
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
	}

	sd, err := servicedirector.NewServiceDirector(ctx, cfg, arch, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize ServiceDirector")
	}

	if err := sd.Start(); err != nil {
		logger.Fatal().Err(err).Msg("Service terminated with an error")
	}
}
