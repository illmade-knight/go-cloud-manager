package deployment

import (
	"context"
	"fmt"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// IDeploymentManager defines the high-level operations for deploying services and their permissions.
type IDeploymentManager interface {
	DeployIAMForDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) error
	// DeployContainersForDataflow(...) will be added later
}

type DeploymentManager struct {
	iamManager IAMManager
	// We may add other managers here later, like a ContainerManager
	logger zerolog.Logger
}

func NewDeploymentManager(iamManager IAMManager, logger zerolog.Logger) *DeploymentManager {
	return &DeploymentManager{
		iamManager: iamManager,
		logger:     logger.With().Str("component", "DeploymentManager").Logger(),
	}
}

// DeployIAMForDataflow orchestrates the IAMManager to apply all IAM policies
// for all services within a single dataflow.
func (dm *DeploymentManager) DeployIAMForDataflow(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, dataflowName string) error {
	dataflow, ok := arch.Dataflows[dataflowName]
	if !ok {
		return fmt.Errorf("dataflow '%s' not found in architecture", dataflowName)
	}

	dm.logger.Info().Str("dataflow", dataflowName).Msg("Deploying IAM for all services in dataflow.")

	// Iterate over all services defined in this dataflow
	for serviceName := range dataflow.Services {
		err := dm.iamManager.ApplyIAMForService(ctx, dataflow, serviceName)
		if err != nil {
			// Fail fast if any service's IAM application fails.
			return fmt.Errorf("failed to apply IAM for service '%s' in dataflow '%s': %w", serviceName, dataflowName, err)
		}
	}

	dm.logger.Info().Str("dataflow", dataflowName).Msg("Successfully deployed IAM for all services in dataflow.")
	return nil
}
