package deployment

import (
	"context"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
)

type ContainerDeployer interface {
	// Deploy creates or updates a service based on its deployment spec.
	Deploy(ctx context.Context, serviceAccountEmail string, spec servicemanager.DeploymentSpec) error
	Teardown(ctx context.Context, serviceName string) error
}
