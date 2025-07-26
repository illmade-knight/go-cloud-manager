package iam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/iam"
	"cloud.google.com/go/iam/apiv1/iampb"
	"github.com/rs/zerolog"
)

// ErrNoAvailableServiceAccountsInPool is returned when the pool is exhausted.
var ErrNoAvailableServiceAccountsInPool = errors.New("no available service accounts in the pool")

// PoolState represents the status of a single service account in our pool.
type PoolState struct {
	Email string
	InUse bool
}

// TestIAMClient implements the IAMClient interface with a stateless pooling mechanism.
type TestIAMClient struct {
	realClient IAMClient
	saManager  *ServiceAccountManager // Kept for the lifetime of the client
	logger     zerolog.Logger
	projectID  string
	prefix     string

	mu   sync.Mutex
	pool map[string]*PoolState // In-memory state for the duration of a single test run.
}

// NewTestIAMClient creates a new client for testing that manages a pool of service accounts.
func NewTestIAMClient(ctx context.Context, projectID string, logger zerolog.Logger, prefix string) (*TestIAMClient, error) {
	realClient, err := NewGoogleIAMClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create real IAM client for test wrapper: %w", err)
	}

	// Create the ServiceAccountManager once and keep it.
	saManager, err := NewServiceAccountManager(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create service account manager: %w", err)
	}

	client := &TestIAMClient{
		realClient: realClient,
		saManager:  saManager,
		logger:     logger.With().Str("component", "TestIAMClient").Logger(),
		projectID:  projectID,
		prefix:     prefix,
		pool:       make(map[string]*PoolState),
	}

	if err := client.initializePool(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize service account pool: %w", err)
	}

	return client, nil
}

// Close cleans roles from all SAs in the pool and closes the underlying real client.
// It no longer needs a context argument.
func (c *TestIAMClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger.Info().Msg("Closing test client and cleaning all service accounts in the pool...")

	var errs []error

	for _, state := range c.pool {
		if !state.InUse {
			continue
		}
		policy, err := c.saManager.GetServiceAccountIAMPolicy(state.Email)
		if err != nil {
			errs = append(errs, fmt.Errorf("could not get policy for %s: %w", state.Email, err))
			continue
		}
		c.logger.Info().Str("account", state.Email).Msg("removing polices")

		if len(policy.InternalProto.Bindings) > 0 {
			c.logger.Info().Str("email", state.Email).Msg("Cleaning roles...")
			newPolicy := &iam.Policy{InternalProto: &iampb.Policy{Etag: policy.InternalProto.Etag}}
			if err := c.saManager.SetServiceAccountIAMPolicy(state.Email, newPolicy); err != nil {
				errs = append(errs, fmt.Errorf("could not clean policy for %s: %w", state.Email, err))
			}
		}
	}

	// Close the managers and clients
	if err := c.saManager.Close(); err != nil {
		errs = append(errs, fmt.Errorf("failed to close service account manager: %w", err))
	}
	if err := c.realClient.Close(); err != nil {
		errs = append(errs, fmt.Errorf("failed to close real client: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered %d errors while closing test client", len(errs))
	}
	return nil
}

// EnsureServiceAccountExists now checks if an available account is "clean" before leasing it.
func (c *TestIAMClient) EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, state := range c.pool {
		if !state.InUse {
			// Check if the account is clean before leasing.
			policy, err := c.saManager.GetServiceAccountIAMPolicy(state.Email)
			if err != nil {
				c.logger.Error().Err(err).Str("email", state.Email).Msg("Could not check policy of available SA, skipping.")
				continue
			}
			if len(policy.InternalProto.Bindings) > 0 {
				c.logger.Warn().Str("email", state.Email).Msg("Found available SA in pool that was not clean, skipping. This may indicate a prior test failed to clean up.")
				continue
			}

			// If we're here, the account is available and clean.
			c.logger.Info().Str("email", state.Email).Msg("Leasing clean service account from pool.")
			state.InUse = true
			return state.Email, nil
		}
	}

	if os.Getenv("TEST_SA_POOL_NO_CREATE") == "true" {
		return "", ErrNoAvailableServiceAccountsInPool
	}

	timestamp := time.Now().Unix()
	poolAccountName := fmt.Sprintf("%s%d", c.prefix, timestamp)
	email, err := c.realClient.EnsureServiceAccountExists(ctx, poolAccountName)
	if err != nil {
		return "", err
	}
	c.pool[email] = &PoolState{Email: email, InUse: true}
	return email, nil
}

// DeleteServiceAccount returns a service account to the pool.
func (c *TestIAMClient) DeleteServiceAccount(ctx context.Context, accountEmail string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state, ok := c.pool[accountEmail]; ok {
		c.logger.Info().Str("email", accountEmail).Msg("Returning service account to the pool (fake delete).")
		state.InUse = false
	}

	return nil
}

// initializePool discovers the pool state directly from GCP.
func (c *TestIAMClient) initializePool(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Info().Msg("Discovering service account pool from GCP...")
	allAccounts, err := c.saManager.ListServiceAccounts()
	if err != nil {
		return fmt.Errorf("failed to list service accounts for pool discovery: %w", err)
	}

	for _, sa := range allAccounts {
		if strings.HasPrefix(strings.Split(sa.Email, "@")[0], c.prefix) {
			c.pool[sa.Email] = &PoolState{Email: sa.Email, InUse: false}
		}
	}
	c.logger.Info().Int("count", len(c.pool)).Str("prefix", c.prefix).Msg("Discovered and initialized pool.")
	return nil
}

// --- Passthrough Methods ---

func (c *TestIAMClient) AddResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error {
	return c.realClient.AddResourceIAMBinding(ctx, binding, member)
}

func (c *TestIAMClient) RemoveResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) error {
	return c.realClient.RemoveResourceIAMBinding(ctx, binding, member)
}

func (c *TestIAMClient) AddArtifactRegistryRepositoryIAMBinding(ctx context.Context, location, repositoryID, role, member string) error {
	return c.realClient.AddArtifactRegistryRepositoryIAMBinding(ctx, location, repositoryID, role, member)
}

func (c *TestIAMClient) AddMemberToServiceAccountRole(ctx context.Context, serviceAccountEmail, member, role string) error {
	return c.realClient.AddMemberToServiceAccountRole(ctx, serviceAccountEmail, member, role)
}
