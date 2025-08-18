package deployment

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"google.golang.org/api/option"
	"google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CloudRunAPI defines the contract for a client that deploys services to Cloud Run.
type CloudRunAPI interface {
	CreateOrUpdateService(ctx context.Context, parent, serviceID string, serviceConfig *run.GoogleCloudRunV2Service) error
}

// googleCloudRunAPIAdapter implements the CloudRunAPI interface using the real Google client.
type googleCloudRunAPIAdapter struct {
	runService *run.Service
	logger     zerolog.Logger
}

// NewGoogleCloudRunAPIAdapter creates the concrete adapter for the Cloud Run API.
// It must be initialized with a specific region, as the Cloud Run Admin API is regional.
func NewGoogleCloudRunAPIAdapter(ctx context.Context, region string, logger zerolog.Logger, opts ...option.ClientOption) (CloudRunAPI, error) {
	endpoint := fmt.Sprintf("%s-run.googleapis.com:443", region)
	runOpts := append(opts, option.WithEndpoint(endpoint))
	runService, err := run.NewService(ctx, runOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloud Run service client: %w", err)
	}
	return &googleCloudRunAPIAdapter{runService: runService, logger: logger}, nil
}

// CreateOrUpdateService checks if a service exists and then either creates a new one
// or patches the existing one with the new configuration.
func (a *googleCloudRunAPIAdapter) CreateOrUpdateService(ctx context.Context, parent, serviceID string, serviceConfig *run.GoogleCloudRunV2Service) error {
	fullServiceName := fmt.Sprintf("%s/services/%s", parent, serviceID)
	_, err := a.runService.Projects.Locations.Services.Get(fullServiceName).Context(ctx).Do()

	var op *run.GoogleLongrunningOperation
	if err != nil {
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.NotFound {
			a.logger.Info().Str("service", serviceID).Msg("Service not found. Creating new Cloud Run service...")
			op, err = a.runService.Projects.Locations.Services.Create(parent, serviceConfig).ServiceId(serviceID).Context(ctx).Do()
		} else {
			return fmt.Errorf("failed to get status of existing Cloud Run service: %w", err)
		}
	} else {
		a.logger.Info().Str("service", serviceID).Msg("Service found. Updating existing Cloud Run service...")
		op, err = a.runService.Projects.Locations.Services.Patch(fullServiceName, serviceConfig).Context(ctx).Do()
	}

	if err != nil {
		return fmt.Errorf("failed to trigger Cloud Run create/update operation: %w", err)
	}

	// Wait for the long-running operation to complete.
	a.logger.Info().Str("operation", op.Name).Msg("Waiting for Cloud Run operation to complete...")
	for {
		getOp, err := a.runService.Projects.Locations.Operations.Get(op.Name).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("failed to poll Cloud Run operation status: %w", err)
		}
		if getOp.Done {
			if getOp.Error != nil {
				return fmt.Errorf("cloud Run operation failed with status: %s", getOp.Error.Message)
			}
			break
		}
		select {
		case <-time.After(2 * time.Second):
			// Continue polling.
		case <-ctx.Done():
			return ctx.Err() // Exit if the context is canceled.
		}
	}

	return nil
}
