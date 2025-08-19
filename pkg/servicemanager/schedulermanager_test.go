package servicemanager_test

import (
	"context"
	"errors"
	"testing"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Implementations ---

// mockSchedulerJob implements the SchedulerJob interface for testing.
type mockSchedulerJob struct {
	id          string
	exists      bool
	existsErr   error
	deleteErr   error
	deleteCalls int
}

func (m *mockSchedulerJob) ID() string                               { return m.id }
func (m *mockSchedulerJob) Exists(ctx context.Context) (bool, error) { return m.exists, m.existsErr }
func (m *mockSchedulerJob) Delete(ctx context.Context) error {
	m.deleteCalls++
	return m.deleteErr
}

// mockSchedulerClient implements the SchedulerClient interface for testing.
type mockSchedulerClient struct {
	jobs         map[string]*mockSchedulerJob
	validateErr  error
	createJobErr error
	createCalls  int
}

func (m *mockSchedulerClient) Job(id string) servicemanager.SchedulerJob {
	if job, ok := m.jobs[id]; ok {
		return job
	}
	// If job doesn't exist in our mock map, return a job that reports it doesn't exist.
	return &mockSchedulerJob{id: id, exists: false}
}

func (m *mockSchedulerClient) CreateJob(ctx context.Context, jobSpec servicemanager.CloudSchedulerJob) (servicemanager.SchedulerJob, error) {
	m.createCalls++
	if m.createJobErr != nil {
		return nil, m.createJobErr
	}
	newJob := &mockSchedulerJob{id: jobSpec.Name, exists: true}
	m.jobs[jobSpec.Name] = newJob
	return newJob, nil
}

func (m *mockSchedulerClient) Close() error { return nil }
func (m *mockSchedulerClient) Validate(resources servicemanager.CloudResourcesSpec) error {
	return m.validateErr
}

// --- Tests ---

func TestCloudSchedulerManager_CreateResources(t *testing.T) {
	t.Parallel()
	logger := zerolog.Nop()
	env := servicemanager.Environment{}
	ctx := context.Background()

	spec := servicemanager.CloudResourcesSpec{
		CloudSchedulerJobs: []servicemanager.CloudSchedulerJob{
			{
				CloudResource: servicemanager.CloudResource{Name: "test-job-1"},
				Schedule:      "0 * * * *",
				TargetService: "test-service",
			},
		},
	}

	t.Run("successfully creates a new job", func(t *testing.T) {
		t.Parallel()
		mockClient := &mockSchedulerClient{
			jobs: make(map[string]*mockSchedulerJob),
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		err = manager.CreateResources(ctx, spec)
		require.NoError(t, err)
		assert.Equal(t, 1, mockClient.createCalls, "Expected CreateJob to be called once")
	})

	t.Run("skips creation if job already exists", func(t *testing.T) {
		t.Parallel()
		mockClient := &mockSchedulerClient{
			jobs: map[string]*mockSchedulerJob{
				"test-job-1": {id: "test-job-1", exists: true},
			},
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		err = manager.CreateResources(ctx, spec)
		require.NoError(t, err)
		assert.Equal(t, 0, mockClient.createCalls, "Expected CreateJob not to be called")
	})

	t.Run("returns error on validation failure", func(t *testing.T) {
		t.Parallel()
		expectedErr := errors.New("validation failed")
		mockClient := &mockSchedulerClient{
			validateErr: expectedErr,
			jobs:        make(map[string]*mockSchedulerJob),
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		err = manager.CreateResources(ctx, spec)
		require.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
		assert.Equal(t, 0, mockClient.createCalls, "Expected CreateJob not to be called on validation error")
	})

	t.Run("returns error on creation failure", func(t *testing.T) {
		t.Parallel()
		expectedErr := errors.New("failed to create")
		mockClient := &mockSchedulerClient{
			createJobErr: expectedErr,
			jobs:         make(map[string]*mockSchedulerJob),
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		err = manager.CreateResources(ctx, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), expectedErr.Error())
		assert.Equal(t, 1, mockClient.createCalls, "Expected CreateJob to be called once even on failure")
	})
}

// REFACTOR: Added tests for the Verify method.
func TestCloudSchedulerManager_Verify(t *testing.T) {
	t.Parallel()
	logger := zerolog.Nop()
	env := servicemanager.Environment{}
	ctx := context.Background()

	spec := servicemanager.CloudResourcesSpec{
		CloudSchedulerJobs: []servicemanager.CloudSchedulerJob{
			{CloudResource: servicemanager.CloudResource{Name: "job-to-verify"}},
		},
	}

	t.Run("successfully verifies an existing job", func(t *testing.T) {
		t.Parallel()
		mockClient := &mockSchedulerClient{
			jobs: map[string]*mockSchedulerJob{
				"job-to-verify": {id: "job-to-verify", exists: true},
			},
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		err = manager.Verify(ctx, spec)
		require.NoError(t, err)
	})

	t.Run("returns error if job does not exist", func(t *testing.T) {
		t.Parallel()
		mockClient := &mockSchedulerClient{
			jobs: make(map[string]*mockSchedulerJob), // Job does not exist in the map
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		err = manager.Verify(ctx, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "job 'job-to-verify' not found")
	})

	t.Run("returns error on client failure", func(t *testing.T) {
		t.Parallel()
		expectedErr := errors.New("client API error")
		mockClient := &mockSchedulerClient{
			jobs: map[string]*mockSchedulerJob{
				"job-to-verify": {id: "job-to-verify", exists: false, existsErr: expectedErr},
			},
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		err = manager.Verify(ctx, spec)
		require.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
	})
}

// REFACTOR: Added tests for the Teardown method.
func TestCloudSchedulerManager_Teardown(t *testing.T) {
	t.Parallel()
	logger := zerolog.Nop()
	env := servicemanager.Environment{}
	ctx := context.Background()

	spec := servicemanager.CloudResourcesSpec{
		CloudSchedulerJobs: []servicemanager.CloudSchedulerJob{
			{CloudResource: servicemanager.CloudResource{Name: "job-to-delete"}},
			{CloudResource: servicemanager.CloudResource{Name: "protected-job", TeardownProtection: true}},
		},
	}

	t.Run("successfully tears down unprotected jobs", func(t *testing.T) {
		t.Parallel()
		jobToDelete := &mockSchedulerJob{id: "job-to-delete", exists: true}
		protectedJob := &mockSchedulerJob{id: "protected-job", exists: true}
		mockClient := &mockSchedulerClient{
			jobs: map[string]*mockSchedulerJob{
				"job-to-delete": jobToDelete,
				"protected-job": protectedJob,
			},
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		err = manager.Teardown(ctx, spec)
		require.NoError(t, err)
		assert.Equal(t, 1, jobToDelete.deleteCalls, "Delete should be called for the unprotected job")
		assert.Equal(t, 0, protectedJob.deleteCalls, "Delete should NOT be called for the protected job")
	})

	t.Run("returns error on client failure", func(t *testing.T) {
		t.Parallel()
		expectedErr := errors.New("client API error")
		jobToDelete := &mockSchedulerJob{id: "job-to-delete", exists: true, deleteErr: expectedErr}
		mockClient := &mockSchedulerClient{
			jobs: map[string]*mockSchedulerJob{
				"job-to-delete": jobToDelete,
			},
		}
		manager, err := servicemanager.NewCloudSchedulerManager(mockClient, logger, env)
		require.NoError(t, err)

		spec := servicemanager.CloudResourcesSpec{
			CloudSchedulerJobs: []servicemanager.CloudSchedulerJob{
				{CloudResource: servicemanager.CloudResource{Name: "job-to-delete"}},
			},
		}

		err = manager.Teardown(ctx, spec)
		require.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
	})
}
