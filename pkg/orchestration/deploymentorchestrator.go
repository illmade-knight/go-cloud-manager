package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
)

// DeploymentManager handles the logic of building and deploying containerized services.
// It has no knowledge of remote orchestration or Pub/Sub.
type DeploymentManager struct {
	arch     *servicemanager.MicroserviceArchitecture
	logger   zerolog.Logger
	deployer deployment.ContainerDeployer
}

// NewDeploymentManager creates a new manager for local build and deploy operations.
func NewDeploymentManager(arch *servicemanager.MicroserviceArchitecture, deployer deployment.ContainerDeployer, logger zerolog.Logger) *DeploymentManager {
	return &DeploymentManager{
		arch:     arch,
		deployer: deployer,
		logger:   logger.With().Str("component", "DeploymentManager").Logger(),
	}
}

// BuildAllServices builds all services defined in the architecture in parallel.
// It returns a map of service names to their final container image URIs.
func (m *DeploymentManager) BuildAllServices(ctx context.Context) (map[string]string, error) {
	m.logger.Info().Msg("Starting parallel build of all services...")
	var wg sync.WaitGroup

	// Collect all services that need building first.
	type buildJob struct {
		Name string
		Spec servicemanager.ServiceSpec
	}
	var jobs []buildJob
	for _, dataflow := range m.arch.Dataflows {
		for sName, sSpec := range dataflow.Services {
			if sSpec.Deployment != nil {
				jobs = append(jobs, buildJob{Name: sName, Spec: sSpec})
			}
		}
	}

	errChan := make(chan error, len(jobs))
	resultChan := make(chan struct{ Name, URI string }, len(jobs))

	// REFACTOR: Launch a goroutine for every service, ensuring true parallelism.
	for _, job := range jobs {
		wg.Add(1)
		go func(j buildJob) {
			defer wg.Done()
			imageURI, err := m.deployer.Build(ctx, j.Name, *j.Spec.Deployment)
			if err != nil {
				errChan <- fmt.Errorf("failed to build '%s': %w", j.Name, err)
				return
			}
			resultChan <- struct{ Name, URI string }{j.Name, imageURI}
		}(job)
	}

	wg.Wait()
	close(errChan)
	close(resultChan)

	// Consolidate results and errors.
	allBuiltImages := make(map[string]string)
	for res := range resultChan {
		allBuiltImages[res.Name] = res.URI
	}

	var buildErrors []string
	for err := range errChan {
		buildErrors = append(buildErrors, err.Error())
	}
	if len(buildErrors) > 0 {
		return nil, fmt.Errorf("encountered errors during service builds: %s", strings.Join(buildErrors, "; "))
	}

	m.logger.Info().Int("image_count", len(allBuiltImages)).Msg("All service builds completed successfully.")
	return allBuiltImages, nil
}

// DeployApplicationServices deploys all services for a given dataflow in parallel.
func (m *DeploymentManager) DeployApplicationServices(ctx context.Context, dataflowName string, saEmails map[string]string, builtImages map[string]string, directorURL string) (map[string]string, error) {
	dataflow, ok := m.arch.Dataflows[dataflowName]
	if !ok {
		return nil, nil // Not an error, just no services to deploy.
	}
	if len(dataflow.Services) == 0 {
		return nil, nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(dataflow.Services))
	urlChan := make(chan struct{ Name, URL string }, len(dataflow.Services))

	for sName, sSpec := range dataflow.Services {
		if sSpec.Deployment == nil {
			continue
		}
		saEmail := saEmails[sName]
		imageURI := builtImages[sName]

		wg.Add(1)
		go func(sn string, spec servicemanager.ServiceSpec, sa, img string) {
			defer wg.Done()
			localSpec := *spec.Deployment
			localSpec.Image = img
			if localSpec.EnvironmentVars == nil {
				localSpec.EnvironmentVars = make(map[string]string)
			}
			localSpec.EnvironmentVars["SERVICE_DIRECTOR_URL"] = directorURL
			localSpec.EnvironmentVars["PROJECT_ID"] = m.arch.ProjectID
			deployedURL, err := m.deployer.DeployService(ctx, sn, sa, localSpec)
			if err != nil {
				errs <- fmt.Errorf("failed to deploy '%s': %w", sn, err)
				return
			}
			urlChan <- struct{ Name, URL string }{sn, deployedURL}
		}(sName, sSpec, saEmail, imageURI)
	}
	wg.Wait()
	close(errs)
	close(urlChan)

	var deploymentErrors []string
	for err := range errs {
		deploymentErrors = append(deploymentErrors, err.Error())
	}
	if len(deploymentErrors) > 0 {
		return nil, fmt.Errorf("errors during deployment for dataflow '%s': %s", dataflowName, strings.Join(deploymentErrors, "; "))
	}

	deployedURLs := make(map[string]string)
	for res := range urlChan {
		deployedURLs[res.Name] = res.URL
	}
	return deployedURLs, nil
}
