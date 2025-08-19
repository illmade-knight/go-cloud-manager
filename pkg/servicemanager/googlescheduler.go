package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	scheduler "cloud.google.com/go/scheduler/apiv1"
	"cloud.google.com/go/scheduler/apiv1/schedulerpb"
	"github.com/robfig/cron/v3"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- Adapter Implementations ---

// gcpSchedulerJobAdapter wraps a *schedulerpb.Job to satisfy the SchedulerJob interface.
type gcpSchedulerJobAdapter struct {
	client   *scheduler.CloudSchedulerClient
	jobName  string // The full resource name of the job
	location string
}

func (a *gcpSchedulerJobAdapter) ID() string {
	return a.jobName
}

func (a *gcpSchedulerJobAdapter) Exists(ctx context.Context) (bool, error) {
	_, err := a.client.GetJob(ctx, &schedulerpb.GetJobRequest{Name: a.jobName})
	if err == nil {
		return true, nil
	}
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	return false, err
}

func (a *gcpSchedulerJobAdapter) Delete(ctx context.Context) error {
	return a.client.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: a.jobName})
}

// gcpSchedulerClientAdapter wraps a *scheduler.CloudSchedulerClient to satisfy the SchedulerClient interface.
type gcpSchedulerClientAdapter struct {
	client    *scheduler.CloudSchedulerClient
	projectID string
	region    string // Default region for jobs
}

func (a *gcpSchedulerClientAdapter) Job(id string) SchedulerJob {
	location := a.region // Assuming jobs are in the default region for simplicity
	fullName := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", a.projectID, location, id)
	return &gcpSchedulerJobAdapter{client: a.client, jobName: fullName, location: location}
}

func (a *gcpSchedulerClientAdapter) CreateJob(ctx context.Context, jobSpec CloudSchedulerJob) (SchedulerJob, error) {
	location := a.region // In a real app, this could come from the jobSpec itself
	parent := fmt.Sprintf("projects/%s/locations/%s", a.projectID, location)

	// The target for a Cloud Scheduler job must be a fully qualified Cloud Run service URL.
	// This assumes the service URL has been hydrated or is discoverable.
	// For this example, we'll construct it based on convention.
	// NOTE: The project ID hash is not predictable. This URL construction is a simplification
	// and in a real system, the hydrated service URL should be passed in.
	targetServiceURL := fmt.Sprintf("https://%s-*.a.run.app", jobSpec.TargetService) // Placeholder

	gcpJob := &schedulerpb.Job{
		Name:        fmt.Sprintf("%s/jobs/%s", parent, jobSpec.Name),
		Description: jobSpec.Description,
		Schedule:    jobSpec.Schedule,
		TimeZone:    jobSpec.TimeZone,
		Target: &schedulerpb.Job_HttpTarget{
			HttpTarget: &schedulerpb.HttpTarget{
				Uri:        targetServiceURL,
				HttpMethod: schedulerpb.HttpMethod_POST,
				Body:       []byte(jobSpec.MessageBody),
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				AuthorizationHeader: &schedulerpb.HttpTarget_OidcToken{
					OidcToken: &schedulerpb.OidcToken{
						ServiceAccountEmail: jobSpec.ServiceAccount,
					},
				},
			},
		},
	}

	createdJob, err := a.client.CreateJob(ctx, &schedulerpb.CreateJobRequest{
		Parent: parent,
		Job:    gcpJob,
	})
	if err != nil {
		return nil, err
	}

	return &gcpSchedulerJobAdapter{client: a.client, jobName: createdJob.Name, location: location}, nil
}

func (a *gcpSchedulerClientAdapter) Close() error {
	return a.client.Close()
}

// Validate checks the scheduler job configuration for valid cron strings and timezones.
func (a *gcpSchedulerClientAdapter) Validate(resources CloudResourcesSpec) error {
	var allErrors []error
	cronParser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	for _, job := range resources.CloudSchedulerJobs {
		if job.Schedule == "" {
			allErrors = append(allErrors, fmt.Errorf("job '%s' has an empty schedule", job.Name))
		} else {
			// Validate the cron expression.
			_, err := cronParser.Parse(job.Schedule)
			if err != nil {
				allErrors = append(allErrors, fmt.Errorf("job '%s' has an invalid cron schedule '%s': %w", job.Name, job.Schedule, err))
			}
		}

		if job.TimeZone != "" {
			// Validate the timezone.
			_, err := time.LoadLocation(job.TimeZone)
			if err != nil {
				allErrors = append(allErrors, fmt.Errorf("job '%s' has an invalid timezone '%s': %w", job.Name, job.TimeZone, err))
			}
		}
	}

	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}
	return nil
}

// --- Factory Function ---

// CreateGoogleSchedulerClient creates a real Cloud Scheduler client wrapped in our interface.
func CreateGoogleSchedulerClient(ctx context.Context, projectID, region string, clientOpts ...option.ClientOption) (SchedulerClient, error) {
	realClient, err := scheduler.NewCloudSchedulerClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("scheduler.NewCloudSchedulerClient: %w", err)
	}
	return &gcpSchedulerClientAdapter{client: realClient, projectID: projectID, region: region}, nil
}
