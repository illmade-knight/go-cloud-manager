package prerequisites

import (
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
)

// PrerequisitePlanner analyzes a MicroserviceArchitecture to determine the set of
// required Google Cloud APIs.
type PrerequisitePlanner struct{}

// NewPlanner creates a new PrerequisitePlanner.
func NewPlanner() *PrerequisitePlanner {
	return &PrerequisitePlanner{}
}

// PlanRequiredServices inspects the architecture and returns a list of required
// service hostnames (e.g., "run.googleapis.com").
func (p *PrerequisitePlanner) PlanRequiredServices(arch *servicemanager.MicroserviceArchitecture) []string {
	// Use a map to automatically handle duplicates.
	requiredAPIs := make(map[string]struct{})

	// Check the service manager spec first.
	if arch.ServiceManagerSpec.Deployment != nil {
		requiredAPIs["run.googleapis.com"] = struct{}{}
		requiredAPIs["cloudbuild.googleapis.com"] = struct{}{}
		requiredAPIs["artifactregistry.googleapis.com"] = struct{}{}
		requiredAPIs["iam.googleapis.com"] = struct{}{} // For service account management
		if len(arch.ServiceManagerSpec.Deployment.SecretEnvironmentVars) > 0 {
			requiredAPIs["secretmanager.googleapis.com"] = struct{}{}
		}
	}

	for _, dataflow := range arch.Dataflows {
		// Check for resources that require APIs.
		resources := dataflow.Resources
		if len(resources.Topics) > 0 || len(resources.Subscriptions) > 0 {
			requiredAPIs["pubsub.googleapis.com"] = struct{}{}
		}
		if len(resources.GCSBuckets) > 0 {
			requiredAPIs["storage.googleapis.com"] = struct{}{}
		}
		if len(resources.BigQueryDatasets) > 0 || len(resources.BigQueryTables) > 0 {
			requiredAPIs["bigquery.googleapis.com"] = struct{}{}
		}

		// Check services within the dataflow.
		for _, service := range dataflow.Services {
			if service.Deployment != nil {
				requiredAPIs["run.googleapis.com"] = struct{}{}
				requiredAPIs["cloudbuild.googleapis.com"] = struct{}{}
				requiredAPIs["artifactregistry.googleapis.com"] = struct{}{}
				if len(service.Deployment.SecretEnvironmentVars) > 0 {
					requiredAPIs["secretmanager.googleapis.com"] = struct{}{}
				}
			}
		}
	}

	// Convert map keys to a slice.
	apiList := make([]string, 0, len(requiredAPIs))
	for api := range requiredAPIs {
		apiList = append(apiList, api)
	}

	return apiList
}
