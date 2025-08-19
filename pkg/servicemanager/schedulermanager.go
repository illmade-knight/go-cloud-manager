package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
)

// CloudSchedulerManager handles the creation and deletion of scheduled jobs.
type CloudSchedulerManager struct {
	client      SchedulerClient
	logger      zerolog.Logger
	environment Environment
}

// NewCloudSchedulerManager creates a new manager for orchestrating scheduler resources.
func NewCloudSchedulerManager(client SchedulerClient, logger zerolog.Logger, environment Environment) (*CloudSchedulerManager, error) {
	if client == nil {
		return nil, errors.New("scheduler client (SchedulerClient interface) cannot be nil")
	}
	return &CloudSchedulerManager{
		client:      client,
		logger:      logger.With().Str("subcomponent", "CloudSchedulerManager").Logger(),
		environment: environment,
	}, nil
}

// CreateResources creates all configured Cloud Scheduler jobs concurrently.
func (m *CloudSchedulerManager) CreateResources(ctx context.Context, resources CloudResourcesSpec) error {
	m.logger.Info().Msg("Starting Cloud Scheduler setup...")

	if err := m.client.Validate(resources); err != nil {
		m.logger.Error().Err(err).Msg("Resource configuration failed validation")
		return err
	}

	var allErrors []error
	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.CloudSchedulerJobs))

	m.logger.Info().Int("count", len(resources.CloudSchedulerJobs)).Msg("Processing scheduler jobs...")
	for _, jobSpec := range resources.CloudSchedulerJobs {
		wg.Add(1)
		go func(spec CloudSchedulerJob) {
			defer wg.Done()
			log := m.logger.With().Str("job", spec.Name).Logger()
			if spec.Name == "" {
				log.Warn().Msg("Skipping creation for job with empty name")
				return
			}
			job := m.client.Job(spec.Name)
			exists, err := job.Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check existence of job '%s': %w", spec.Name, err)
				return
			}
			if exists {
				log.Info().Msg("Job already exists, skipping creation.")
				// NOTE: Update logic could be added here if needed.
				return
			}
			log.Info().Msg("Creating job...")
			_, createErr := m.client.CreateJob(ctx, spec)
			if createErr != nil {
				errChan <- fmt.Errorf("failed to create job '%s': %w", spec.Name, createErr)
			} else {
				log.Info().Msg("Job created successfully.")
			}
		}(jobSpec)
	}
	wg.Wait()
	close(errChan)

	for err := range errChan {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}

	m.logger.Info().Msg("Cloud Scheduler setup completed successfully.")
	return nil
}

// Teardown deletes all specified Cloud Scheduler jobs concurrently.
func (m *CloudSchedulerManager) Teardown(ctx context.Context, resources CloudResourcesSpec) error {
	m.logger.Info().Int("count", len(resources.CloudSchedulerJobs)).Msg("Tearing down Cloud Scheduler jobs...")
	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.CloudSchedulerJobs))

	for _, jobSpec := range resources.CloudSchedulerJobs {
		wg.Add(1)
		go func(spec CloudSchedulerJob) {
			defer wg.Done()
			log := m.logger.With().Str("job", spec.Name).Logger()
			if spec.TeardownProtection {
				log.Warn().Msg("Teardown protection enabled, skipping deletion.")
				return
			}
			if spec.Name == "" {
				return
			}
			log.Info().Msg("Attempting to delete job...")
			err := m.client.Job(spec.Name).Delete(ctx)
			// REFACTOR: Use the isNotFound helper to avoid a direct dependency on the gRPC status package.
			if err != nil && !isNotFound(err) {
				errChan <- fmt.Errorf("failed to delete job %s: %w", spec.Name, err)
			}
		}(jobSpec)
	}
	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}
	return errors.Join(allErrors...)
}

// REFACTOR: Added the missing Verify method to conform to the IManager interface.
// Verify checks for the existence of all configured Cloud Scheduler jobs.
func (m *CloudSchedulerManager) Verify(ctx context.Context, resources CloudResourcesSpec) error {
	m.logger.Info().Int("count", len(resources.CloudSchedulerJobs)).Msg("Verifying Cloud Scheduler jobs...")
	var wg sync.WaitGroup
	errChan := make(chan error, len(resources.CloudSchedulerJobs))

	for _, jobSpec := range resources.CloudSchedulerJobs {
		wg.Add(1)
		go func(spec CloudSchedulerJob) {
			defer wg.Done()
			exists, err := m.client.Job(spec.Name).Exists(ctx)
			if err != nil {
				errChan <- fmt.Errorf("failed to check job '%s': %w", spec.Name, err)
			} else if !exists {
				errChan <- fmt.Errorf("job '%s' not found", spec.Name)
			}
		}(jobSpec)
	}

	wg.Wait()
	close(errChan)

	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}
	if len(allErrors) > 0 {
		return fmt.Errorf("Cloud Scheduler verification failed: %w", errors.Join(allErrors...))
	}

	m.logger.Info().Msg("Cloud Scheduler verification completed successfully.")
	return nil
}
