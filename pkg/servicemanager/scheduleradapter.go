package servicemanager

import (
	"context"
)

// --- Scheduler Client Abstraction Interfaces ---

// SchedulerJob defines the interface for a single scheduled job.
type SchedulerJob interface {
	ID() string
	Exists(ctx context.Context) (bool, error)
	Delete(ctx context.Context) error
	// Note: Update is often complex for schedulers and can be added later if needed.
}

// SchedulerClient defines the fully generic interface for a cloud scheduler client.
type SchedulerClient interface {
	Job(id string) SchedulerJob
	CreateJob(ctx context.Context, jobSpec CloudSchedulerJob) (SchedulerJob, error)
	Close() error
	// Validate checks if the resource configuration is valid for the specific implementation.
	Validate(resources CloudResourcesSpec) error
}
