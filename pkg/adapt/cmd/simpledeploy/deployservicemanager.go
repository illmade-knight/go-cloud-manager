package main

import (
	"context"
	"github.com/illmade-knight/go-iot-dataflows/deployment/cloudrun"
	"github.com/illmade-knight/go-iot-dataflows/deployment/config"
	"github.com/illmade-knight/go-iot-dataflows/deployment/iam"
	"github.com/illmade-knight/go-iot-dataflows/deployment/orchestrator"
	"github.com/illmade-knight/go-iot/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"os"
)

func main() {

	smc := &servicemanager.TopLevelConfig{
		ResourceGroup: servicemanager.ResourceGroup{
			Name:         "servicemanager",
			Description:  "the service manager",
			ServiceNames: nil,
			Lifecycle:    nil,
			Resources: servicemanager.ResourcesSpec{
				Topics:           nil,
				Subscriptions:    nil,
				BigQueryDatasets: nil,
				BigQueryTables:   nil,
				GCSBuckets:       nil,
			},
		},
		DefaultProjectID: "gemini-power-test",
		DefaultLocation:  "eu-west-1",
		DefaultRegion:    "eu-west-1",
		Environments: map[string]servicemanager.EnvironmentSpec{"servicemanager": servicemanager.EnvironmentSpec{
			ProjectID:          "",
			DefaultLocation:    "",
			DefaultRegion:      "",
			DefaultLabels:      nil,
			TeardownProtection: false,
		}},
		Services: []servicemanager.ServiceSpec{servicemanager.ServiceSpec{
			Name:           "",
			Description:    "",
			ServiceAccount: "",
			SourcePath:     "",
			MinInstances:   0,
			Metadata:       nil,
			HealthCheck:    nil,
		}},
		Dataflows: nil,
	}

	dc := &config.DeployerConfig{
		Region:                     "eu-west-1",
		ServiceSourcePath:          "dataflow/devflow/",
		ServiceDirectorServiceName: "servicedirector",
		ProjectID:                  "gemini-power-test",
		DefaultDockerRegistry:      "gcr.io/gemini-power-test",
		DirectorServiceURL:         "",
		CloudRunDefaults:           config.CloudRunSetup{},
		Services:                   nil,
	}

	logger := log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	ctx := context.Background()

	// 5. Initialize clients
	cloudRunLogger := logger.With().Str("component", "cloudRunClient").Logger()
	// Ensure NewClient uses deployerCfg directly for project ID and region
	cloudRunClient, err := cloudrun.NewClient(ctx, dc, cloudRunLogger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create cloudRun client")
	}
	logger.Info().Msg("cloud run client created")
	// Use deployerCfg.ProjectID for IAM client. Note: user's original main.go snippet used topLevelConfig.DefaultProjectID here.
	// Sticking to deployerCfg.ProjectID as it should be the primary config for the orchestrator.
	iamClient, err := iam.NewClient(ctx, dc.ProjectID)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create IAM client")
	}
	logger.Info().Msg("iam client created")

	// 6. Create Coordinator instance
	coordinater := orchestrator.NewCoordinater(
		cloudRunClient,
		iamClient,
		logger,
	)

	sdc := orchestrator.ServiceDeploymentConfig{
		ServiceName: "servicemanager",
		DockerConfig: orchestrator.DockerDeploymentConfig{
			SourcePath: "dataflow/devflow/",
		},
		CloudrunConfig: config.GetDefaultCloudRunSetup(),
	}

	err = coordinater.SerialDeploy(ctx, smc, sdc)
	if err != nil {
		return
	}
}
