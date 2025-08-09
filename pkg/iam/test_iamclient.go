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

// ErrNoAvailableServiceAccountsInPool is returned by EnsureServiceAccountExists when the pool is
// configured with TEST_SA_POOL_NO_CREATE=true and all available accounts are in use.
var ErrNoAvailableServiceAccountsInPool = errors.New("no available service accounts in the pool")

// PoolState represents the status of a single service account in the test pool.
type PoolState struct {
	Email string
	InUse bool
}

// TestIAMClient implements the IAMClient interface with a service account pooling mechanism.
// This client is designed for integration tests to mitigate IAM API quota limitations.
// Instead of creating and deleting service accounts for each test run, it discovers a pool
// of pre-existing service accounts (identified by a common prefix) and "leases" them to tests.
//
// The "delete" operation is faked; it simply returns the account to the pool. The Close method
// is responsible for cleaning all IAM roles from the leased accounts, making them pristine for the next run.
type TestIAMClient struct {
	realClient IAMClient
	saManager  *ServiceAccountManager
	logger     zerolog.Logger
	projectID  string
	prefix     string

	mu   sync.Mutex
	pool map[string]*PoolState // In-memory state for the duration of a single test run.
}

// NewTestIAMClient creates a new client for testing that manages a pool of service accounts.
// It discovers all service accounts in the project with the given prefix and initializes the pool.
func NewTestIAMClient(ctx context.Context, projectID string, logger zerolog.Logger, prefix string) (*TestIAMClient, error) {
	realClient, err := NewGoogleIAMClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create real IAM client for test wrapper: %w", err)
	}

	saManager, err := NewServiceAccountManager(ctx, projectID, logger)
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

	err = client.initializePool(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize service account pool: %w", err)
	}

	return client, nil
}

// Close cleans all IAM roles from any service accounts used during the test run
// and closes the underlying client connections.
func (c *TestIAMClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger.Info().Msg("Closing test client and cleaning all service accounts in the pool...")

	// Create a short-lived context specifically for the cleanup operations.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var errs []error

	for _, state := range c.pool {
		if !state.InUse {
			continue // Skip accounts that were not used in this test run.
		}
		policy, err := c.saManager.GetServiceAccountIAMPolicy(cleanupCtx, state.Email)
		if err != nil {
			errs = append(errs, fmt.Errorf("could not get policy for %s: %w", state.Email, err))
			continue
		}

		if len(policy.InternalProto.Bindings) > 0 {
			c.logger.Info().Str("email", state.Email).Msg("Cleaning roles from service account...")
			// Create a new, empty policy, preserving the ETag for optimistic concurrency control.
			newPolicy := &iam.Policy{InternalProto: &iampb.Policy{Etag: policy.InternalProto.Etag}}
			err = c.saManager.SetServiceAccountIAMPolicy(cleanupCtx, state.Email, newPolicy)
			if err != nil {
				errs = append(errs, fmt.Errorf("could not clean policy for %s: %w", state.Email, err))
			}
		}
	}

	// Close the managers and clients.
	if err := c.saManager.Close(); err != nil {
		errs = append(errs, fmt.Errorf("failed to close service account manager: %w", err))
	}
	if err := c.realClient.Close(); err != nil {
		errs = append(errs, fmt.Errorf("failed to close real client: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered %d errors while closing test client: %v", len(errs), errs)
	}
	return nil
}

// EnsureServiceAccountExists leases an available and "clean" (no IAM bindings) service account from the pool.
// If no clean accounts are available and TEST_SA_POOL_NO_CREATE is not "true", it will create a new one.
func (c *TestIAMClient) EnsureServiceAccountExists(ctx context.Context, accountName string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, state := range c.pool {
		if !state.InUse {
			// Check if the account is clean before leasing.
			policy, err := c.saManager.GetServiceAccountIAMPolicy(ctx, state.Email)
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

	// If we've exhausted the pool of clean accounts.
	if os.Getenv("TEST_SA_POOL_NO_CREATE") == "true" {
		c.logger.Error().Msg("No available service accounts in the pool and creation is disabled.")
		return "", ErrNoAvailableServiceAccountsInPool
	}

	// Create a new account to add to the pool.
	c.logger.Info().Msg("No clean service accounts available in pool, creating a new one.")
	timestamp := time.Now().Unix()
	poolAccountName := fmt.Sprintf("%s%d", c.prefix, timestamp)
	email, err := c.realClient.EnsureServiceAccountExists(ctx, poolAccountName)
	if err != nil {
		return "", err
	}
	c.pool[email] = &PoolState{Email: email, InUse: true}
	return email, nil
}

// DeleteServiceAccount is a "fake" delete that simply returns a service account to the pool,
// making it available for the next test that needs one.
func (c *TestIAMClient) DeleteServiceAccount(ctx context.Context, accountEmail string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state, ok := c.pool[accountEmail]; ok {
		c.logger.Info().Str("email", accountEmail).Msg("Returning service account to the pool (fake delete).")
		state.InUse = false
	} else {
		c.logger.Warn().Str("email", accountEmail).Msg("Attempted to fake-delete a service account not tracked in the pool.")
	}

	return nil
}

// initializePool discovers the pool state by listing all service accounts in the project
// and adding those with the configured prefix to the in-memory pool.
func (c *TestIAMClient) initializePool(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Info().Msg("Discovering service account pool from GCP...")
	allAccounts, err := c.saManager.ListServiceAccounts(ctx)
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
// These methods delegate directly to the real IAM client, as they don't involve
// the service account lifecycle managed by the pool.

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

func (c *TestIAMClient) CheckResourceIAMBinding(ctx context.Context, binding IAMBinding, member string) (bool, error) {
	return c.realClient.CheckResourceIAMBinding(ctx, binding, member)
}
