package prerequisites

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	"cloud.google.com/go/serviceusage/apiv1"
	"cloud.google.com/go/serviceusage/apiv1/serviceusagepb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// ServiceAPIClient defines the contract for a client that can check for and
// enable cloud service APIs.
type ServiceAPIClient interface {
	GetEnabledServices(ctx context.Context, projectID string) (map[string]struct{}, error)
	EnableServices(ctx context.Context, projectID string, services []string) error
	Close() error
}

// googleServiceAPIClient implements the ServiceAPIClient interface for GCP.
type googleServiceAPIClient struct {
	client *serviceusage.Client
	logger zerolog.Logger
}

// NewGoogleServiceAPIClient creates a new client for the GCP Service Usage API.
func NewGoogleServiceAPIClient(ctx context.Context, logger zerolog.Logger, opts ...option.ClientOption) (ServiceAPIClient, error) {
	client, err := serviceusage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create serviceusage client: %w", err)
	}
	return &googleServiceAPIClient{client: client, logger: logger}, nil
}

// GetEnabledServices retrieves a set of all currently enabled APIs for a project.
func (c *googleServiceAPIClient) GetEnabledServices(ctx context.Context, projectID string) (map[string]struct{}, error) {
	enabled := make(map[string]struct{})
	req := &serviceusagepb.ListServicesRequest{
		Parent: fmt.Sprintf("projects/%s", projectID),
		Filter: "state:ENABLED",
	}
	it := c.client.ListServices(ctx, req)
	for {
		service, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list enabled services: %w", err)
		}
		// The service name is in the format "projects/12345/services/foo.googleapis.com"
		// We only need the "foo.googleapis.com" part.
		lastSlash := strings.LastIndex(service.Name, "/")
		if lastSlash != -1 {
			enabled[service.Name[lastSlash+1:]] = struct{}{}
		}
	}
	return enabled, nil
}

// EnableServices enables a batch of services and waits for the operation to complete.
func (c *googleServiceAPIClient) EnableServices(ctx context.Context, projectID string, services []string) error {
	req := &serviceusagepb.BatchEnableServicesRequest{
		Parent:     fmt.Sprintf("projects/%s", projectID),
		ServiceIds: services,
	}
	op, err := c.client.BatchEnableServices(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to start batch enable services operation: %w", err)
	}
	res, err := op.Wait(ctx)
	if err != nil {
		return err
	}
	c.logger.Info().Int("enabled", len(res.Services)).Strs("services", services).Msg("Successfully enabled services.")
	return nil
}

func (c *googleServiceAPIClient) Close() error {
	return c.client.Close()
}
