package main

import (
	"context"
	_ "embed" // REFACTOR: Import the embed package
	"os"

	"github.com/illmade-knight/go-cloud-manager/microservice/servicedirector"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

// REFACTOR: This directive tells the Go compiler to embed the contents of
// services.yaml into the servicesYAML byte slice at build time.
//
//go:embed services.yaml
var servicesYAML []byte

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

	sd, err := servicedirector.NewServiceDirector(ctx, cfg, arch, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize ServiceDirector")
	}

	if err := sd.Start(); err != nil {
		logger.Fatal().Err(err).Msg("Service terminated with an error")
	}
}
