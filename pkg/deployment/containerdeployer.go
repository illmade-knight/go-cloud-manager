package deployment

import (
	"context"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
)

type ContainerDeployer interface {
	// Build handles all steps to produce a container image, returning the image URI.
	Build(ctx context.Context, serviceName string, spec servicemanager.DeploymentSpec) (string, error)

	// DeployService deploys a pre-built image to the target platform, returning the service URL.
	DeployService(ctx context.Context, serviceName, serviceAccountEmail string, spec servicemanager.DeploymentSpec) (string, error)

	// Deploy creates or updates a service based on its deployment spec.
	Deploy(ctx context.Context, serviceName, serviceAccountEmail string, spec servicemanager.DeploymentSpec) (string, error)
	Teardown(ctx context.Context, serviceName string) error
}
