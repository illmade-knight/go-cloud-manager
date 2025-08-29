package orchestration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/deployment"
	"github.com/illmade-knight/go-cloud-manager/pkg/iam"
	"github.com/illmade-knight/go-cloud-manager/pkg/prerequisites"
	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

// PlanEntry is a richer struct for the IAM plan file, including the reason for the binding.
type PlanEntry struct {
	Source  string         `yaml:"source"`
	Binding iam.IAMBinding `yaml:"binding"`
	Reason  string         `yaml:"reason"`
}

// PrepareServiceDirectorSource marshals the arch to YAML and copies the resulting services.yaml file
// into the ServiceDirector's source code directory so it can be included in the build.
func PrepareServiceDirectorSource(arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger) error {
	yamlBytes, err := yaml.Marshal(arch)
	if err != nil {
		return fmt.Errorf("failed to marshal hydrated architecture to YAML: %w", err)
	}
	if arch.ServiceManagerSpec.Deployment == nil || arch.ServiceManagerSpec.Deployment.BuildableModulePath == "" {
		return errors.New("ServiceManagerSpec.Deployment.BuildableModulePath is not defined")
	}
	directorSourcePath := arch.ServiceManagerSpec.Deployment.SourcePath
	directorBuildPath := arch.ServiceManagerSpec.Deployment.BuildableModulePath
	destinationPath := filepath.Join(directorSourcePath, directorBuildPath, "services.yaml")
	logger.Info().Str("destination", destinationPath).Msg("Copying hydrated services.yaml to ServiceDirector source...")
	err = os.WriteFile(destinationPath, yamlBytes, 0644)
	if err != nil {
		return fmt.Errorf("failed to write services.yaml to %s: %w", destinationPath, err)
	}
	logger.Info().Msg("✅ Successfully copied services.yaml.")
	return nil
}

// ConductorOptions provides flags and configuration to control the orchestration workflow.
type ConductorOptions struct {
	CheckPrerequisites      bool
	PreflightServiceConfigs bool
	SetupIAM                bool
	BuildAndDeployServices  bool
	TriggerRemoteSetup      bool
	VerifyDataflowIAM       bool
	DirectorURLOverride     string
	SAPollTimeout           time.Duration
	PolicyPollTimeout       time.Duration
}

// Conductor manages the high-level orchestration of a full microservice architecture deployment.
type Conductor struct {
	arch                 *servicemanager.MicroserviceArchitecture
	logger               zerolog.Logger
	prerequisitesManager *prerequisites.Manager
	iamOrch              *IAMOrchestrator
	deploymentManager    *DeploymentManager
	remoteDirectorClient *RemoteDirectorClient
	preflightValidator   *PreflightValidator // REFACTOR: Added field for the new validator.
	options              ConductorOptions
	serviceAccountEmails map[string]string
	directorURL          string
	deployedServiceURLs  map[string]string
	builtImageURIs       map[string]string
}

// NewConductor creates and initializes a new Conductor with specific options.
func NewConductor(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, opts ConductorOptions) (*Conductor, error) {
	prereqClient, err := prerequisites.NewGoogleServiceAPIClient(ctx, logger)
	if err != nil {
		return nil, err
	}
	iamOrch, err := NewIAMOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, err
	}

	projectID := arch.ProjectID
	region := arch.Region
	sourceBucket := fmt.Sprintf("%s_cloudbuild", projectID)
	deployer, err := deployment.NewCloudBuildDeployer(ctx, projectID, region, sourceBucket, logger)
	if err != nil {
		return nil, err
	}
	deploymentManager := NewDeploymentManager(arch, deployer, logger)
	remoteClient, err := NewRemoteDirectorClient(ctx, arch, logger)
	if err != nil {
		return nil, err
	}

	return &Conductor{
		arch:                 arch,
		logger:               logger.With().Str("component", "Conductor").Logger(),
		prerequisitesManager: prerequisites.NewManager(prereqClient, logger),
		iamOrch:              iamOrch,
		deploymentManager:    deploymentManager,
		remoteDirectorClient: remoteClient,
		preflightValidator:   NewPreflightValidator(arch, logger), // REFACTOR: Initialize the validator.
		options:              opts,
		serviceAccountEmails: make(map[string]string),
		deployedServiceURLs:  make(map[string]string),
		builtImageURIs:       make(map[string]string),
	}, nil
}

// Preflight runs permission checks for the identity executing the Conductor.
func (c *Conductor) Preflight(ctx context.Context) error {
	if err := c.iamOrch.PreflightChecks(ctx); err != nil {
		return fmt.Errorf("preflight permission check failed: %w", err)
	}
	return nil
}

// GenerateIAMPlan creates the full IAM plan and writes it to a YAML file.
func (c *Conductor) GenerateIAMPlan(iamPlanYAML string) error {
	c.logger.Info().Msg("Generating full IAM plan...")
	planner := iam.NewRolePlanner(c.logger)
	var fullPlan []PlanEntry

	directorBindings, err := planner.PlanRolesForServiceDirector(c.arch)
	if err != nil {
		return fmt.Errorf("failed to plan ServiceDirector roles: %w", err)
	}
	for _, binding := range directorBindings {
		fullPlan = append(fullPlan, PlanEntry{
			Source:  "ServiceDirector",
			Binding: binding,
			Reason:  "Administrative role required by the ServiceDirector to manage dataflow resources.",
		})
	}

	appBindings, err := planner.PlanRolesForApplicationServices(c.arch)
	if err != nil {
		return fmt.Errorf("failed to plan application service roles: %w", err)
	}
	for _, binding := range appBindings {
		fullPlan = append(fullPlan, PlanEntry{
			Source:  "ApplicationService",
			Binding: binding,
			Reason:  fmt.Sprintf("Needed because of the service-to-resource link defined in services.yaml for '%s'.", binding.ServiceAccount),
		})
	}

	yamlBytes, err := yaml.Marshal(fullPlan)
	if err != nil {
		return fmt.Errorf("failed to marshal IAM plan to YAML: %w", err)
	}

	err = os.WriteFile(iamPlanYAML, yamlBytes, 0644)
	if err != nil {
		return fmt.Errorf("failed to write IAM plan to file: %w", err)
	}

	c.logger.Info().Str("file", iamPlanYAML).Int("bindings", len(fullPlan)).Msg("✅ Successfully generated IAM plan.")
	return nil
}

// Run executes the full, multi-phase orchestration workflow in the correct sequence.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration...")

	if err := c.checkPrerequisites(ctx); err != nil {
		return err
	}
	if err := c.prepareBuildArtifactsPhase(); err != nil {
		return err
	}
	if err := c.preflightServiceConfigsPhase(); err != nil {
		return err
	}
	if err := c.setupIAMPhase(ctx); err != nil {
		return err
	}
	if err := c.buildAndDeployPhase(ctx); err != nil {
		return err
	}
	if err := c.triggerRemoteFoundationalSetup(ctx); err != nil {
		return err
	}
	if err := c.verifyResourceLevelIAMPhase(ctx); err != nil {
		return err
	}
	if err := c.deploymentPhase(ctx); err != nil {
		return err
	}
	if err := c.triggerRemoteDependentSetup(ctx); err != nil {
		return err
	}

	c.logger.Info().Msg("✅ Full architecture orchestration completed successfully.")
	return nil
}

func (c *Conductor) checkPrerequisites(ctx context.Context) error {
	if !c.options.CheckPrerequisites {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 0: Checking prerequisite service APIs...")
	err := c.prerequisitesManager.CheckAndEnable(ctx, c.arch)
	if err != nil {
		return fmt.Errorf("failed during prerequisite API check: %w", err)
	}
	return nil
}

func (c *Conductor) setupIAMPhase(ctx context.Context) error {
	if !c.options.SetupIAM {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 1: Setting up all service accounts and project-level IAM...")
	sdSaEmails, err := c.iamOrch.SetupServiceDirectorIAM(ctx)
	if err != nil {
		return err
	}
	err = c.iamOrch.VerifyServiceDirectorIAM(ctx, sdSaEmails, c.options.PolicyPollTimeout)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector IAM verification: %w", err)
	}
	for k, v := range sdSaEmails {
		c.serviceAccountEmails[k] = v
	}

	var allAppSaEmails []string
	var allDataflowEmails = make(map[string]map[string]string)

	for dataflowName := range c.arch.Dataflows {
		dfSaEmails, err := c.iamOrch.EnsureDataflowSAsExist(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed during SA setup for dataflow '%s': %w", dataflowName, err)
		}
		allDataflowEmails[dataflowName] = dfSaEmails
		for k, v := range dfSaEmails {
			c.serviceAccountEmails[k] = v
			allAppSaEmails = append(allAppSaEmails, v)
		}
	}

	c.logger.Info().Int("count", len(allAppSaEmails)).Msg("Verifying propagation of newly created service accounts...")
	for _, email := range allAppSaEmails {
		err = c.iamOrch.PollForSAExistence(ctx, email, c.options.SAPollTimeout)
		if err != nil {
			return fmt.Errorf("failed while waiting for SA '%s' to propagate: %w", email, err)
		}
	}
	c.logger.Info().Msg("All service accounts have propagated.")

	c.logger.Info().Msg("Applying and verifying project-level IAM roles for dataflows...")
	for dataflowName, saEmails := range allDataflowEmails {
		err = c.iamOrch.ApplyProjectLevelIAMForDataflow(ctx, dataflowName, saEmails)
		if err != nil {
			return fmt.Errorf("failed during project-level IAM application for dataflow '%s': %w", dataflowName, err)
		}
		err = c.iamOrch.VerifyProjectLevelIAMForDataflow(ctx, dataflowName, saEmails, c.options.PolicyPollTimeout)
		if err != nil {
			return fmt.Errorf("failed during project-level IAM verification for dataflow '%s': %w", dataflowName, err)
		}
	}

	c.logger.Info().Msg("Phase 1: All service accounts and project-level IAM are set up and verified.")
	return nil
}

// In orchestration/conductor.go
func (c *Conductor) prepareBuildArtifactsPhase() error {
	c.logger.Info().Msg("Executing Phase 1.5: Preparing build artifacts (services.yaml, resources.yaml)...")

	// REFACTOR: First, clean up any leftover YAML files from previous runs.
	if err := CleanStaleConfigs(c.arch, c.logger); err != nil {
		return fmt.Errorf("failed during config cleanup: %w", err)
	}

	serviceConfigs, err := GenerateServiceConfigs(c.arch)
	if err != nil {
		return fmt.Errorf("failed to generate service-specific configs: %w", err)
	}

	if err := WriteServiceConfigFiles(serviceConfigs, c.logger); err != nil {
		return fmt.Errorf("failed to write service config files: %w", err)
	}

	// NEW STEP
	// This new function will handle the copying of files like routes.yaml.
	if err := GenerateAndWriteServiceSpecificConfigs(c.arch, c.logger); err != nil {
		return fmt.Errorf("failed to generate service-specific configs: %w", err)
	}

	if err := PrepareServiceDirectorSource(c.arch, c.logger); err != nil {
		return fmt.Errorf("failed to prepare ServiceDirector source: %w", err)
	}

	c.logger.Info().Msg("Phase 1.5: Build artifacts prepared successfully.")
	return nil
}

// preflightServiceConfigsPhase is now a simple wrapper around the PreflightValidator.
func (c *Conductor) preflightServiceConfigsPhase() error {
	if !c.options.PreflightServiceConfigs {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 1.6: Running local preflight validation...")
	return c.preflightValidator.Run()
}

func (c *Conductor) buildAndDeployPhase(ctx context.Context) error {
	if !c.options.BuildAndDeployServices {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 2: Building all services and deploying director...")

	builtImages, err := c.deploymentManager.BuildAllServices(ctx)
	if err != nil {
		return err
	}
	c.builtImageURIs = builtImages

	c.logger.Info().Msg("Deploying ServiceDirector...")
	if c.options.DirectorURLOverride != "" {
		c.directorURL = c.options.DirectorURLOverride
		c.logger.Info().Str("url", c.directorURL).Msg("Using Director URL override.")
	} else {
		serviceName := c.arch.ServiceManagerSpec.Name
		saEmail, ok := c.serviceAccountEmails[serviceName]
		if !ok {
			return fmt.Errorf("SA email not found for ServiceDirector '%s'", serviceName)
		}
		deploymentSpec := c.arch.ServiceManagerSpec.Deployment
		if deploymentSpec == nil {
			return errors.New("ServiceManagerSpec.Deployment is not defined")
		}
		url, deployErr := c.deploymentManager.deployer.BuildAndDeploy(ctx, serviceName, saEmail, *deploymentSpec)
		if deployErr != nil {
			return fmt.Errorf("failed during ServiceDirector deployment: %w", deployErr)
		}
		c.directorURL = url
	}

	c.logger.Info().Msg("Phase 2: Build and director deployment completed successfully.")
	return nil
}

func (c *Conductor) triggerRemoteFoundationalSetup(ctx context.Context) error {
	if !c.options.TriggerRemoteSetup {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 3: Triggering remote foundational resource setup for all dataflows in parallel...")
	var wg sync.WaitGroup
	errChan := make(chan error, len(c.arch.Dataflows))

	for dataflowName := range c.arch.Dataflows {
		wg.Add(1)
		go func(dfName string) {
			defer wg.Done()
			_, err := c.remoteDirectorClient.TriggerFoundationalSetup(ctx, dfName)
			if err != nil {
				errChan <- fmt.Errorf("failed during foundational setup for dataflow '%s': %w", dfName, err)
			}
		}(dataflowName)
	}

	wg.Wait()
	close(errChan)
	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}
	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}

	c.logger.Info().Msg("Phase 3: Remote foundational resource setup is complete.")
	return nil
}

func (c *Conductor) verifyResourceLevelIAMPhase(ctx context.Context) error {
	if !c.options.VerifyDataflowIAM {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 4: Verifying dataflow RESOURCE-LEVEL IAM policy propagation...")
	for dataflowName := range c.arch.Dataflows {
		err := c.iamOrch.VerifyResourceLevelIAMForDataflow(ctx, dataflowName, c.serviceAccountEmails, c.options.PolicyPollTimeout)
		if err != nil {
			return fmt.Errorf("failed during resource-level IAM verification for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Phase 4: Dataflow resource-level IAM verification complete.")
	return nil
}

func (c *Conductor) deploymentPhase(ctx context.Context) error {
	if !c.options.BuildAndDeployServices {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 5: Deploying final application services for all dataflows in parallel...")
	if c.directorURL == "" {
		return errors.New("cannot deploy dataflow services: director URL is not available")
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(c.arch.Dataflows))
	var mu sync.Mutex // To safely write to the shared deployedServiceURLs map

	for dataflowName := range c.arch.Dataflows {
		wg.Add(1)
		go func(dfName string) {
			defer wg.Done()
			deployedURLs, err := c.deploymentManager.DeployApplicationServices(ctx, dfName, c.serviceAccountEmails, c.builtImageURIs, c.directorURL)
			if err != nil {
				errChan <- err
				return
			}
			mu.Lock()
			for service, url := range deployedURLs {
				c.deployedServiceURLs[service] = url
			}
			mu.Unlock()
		}(dataflowName)
	}

	wg.Wait()
	close(errChan)
	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}
	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}

	c.logger.Info().Msg("Phase 5: Final deployment of all dataflow services is complete.")
	return nil
}

func (c *Conductor) triggerRemoteDependentSetup(ctx context.Context) error {
	if !c.options.TriggerRemoteSetup {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 6: Triggering remote dependent resource setup...")

	var wg sync.WaitGroup
	errChan := make(chan error, len(c.arch.Dataflows))

	for dataflowName, dataflow := range c.arch.Dataflows {
		if len(dataflow.Resources.CloudSchedulerJobs) == 0 {
			continue
		}

		wg.Add(1)
		go func(dfName string, df servicemanager.ResourceGroup) {
			defer wg.Done()
			dataflowServiceURLs := make(map[string]string)
			for _, job := range df.Resources.CloudSchedulerJobs {
				if url, ok := c.deployedServiceURLs[job.TargetService]; ok {
					dataflowServiceURLs[job.TargetService] = url
				} else {
					errChan <- fmt.Errorf("consistency error: URL for target service '%s' not found for job '%s'", job.TargetService, job.Name)
					return
				}
			}

			_, err := c.remoteDirectorClient.TriggerDependentSetup(ctx, dfName, dataflowServiceURLs)
			if err != nil {
				errChan <- fmt.Errorf("failed during dependent setup for dataflow '%s': %w", dfName, err)
			}
		}(dataflowName, dataflow)
	}

	wg.Wait()
	close(errChan)
	var allErrors []error
	for err := range errChan {
		allErrors = append(allErrors, err)
	}
	if len(allErrors) > 0 {
		return errors.Join(allErrors...)
	}

	c.logger.Info().Msg("Phase 6: Dependent resource setup is complete.")
	return nil
}

// Teardown cleans up all resources and services created by the Conductor.
func (c *Conductor) Teardown(ctx context.Context) error {
	c.logger.Info().Msg("--- Starting Conductor Teardown ---")
	var allErrors []error

	for _, dataflow := range c.arch.Dataflows {
		for serviceName := range dataflow.Services {
			if err := c.deploymentManager.deployer.Teardown(ctx, serviceName); err != nil {
				allErrors = append(allErrors, fmt.Errorf("failed to teardown service '%s': %w", serviceName, err))
			}
		}
	}

	if err := c.deploymentManager.deployer.Teardown(ctx, c.arch.ServiceManagerSpec.Name); err != nil {
		allErrors = append(allErrors, fmt.Errorf("failed to teardown service director: %w", err))
	}

	if err := c.remoteDirectorClient.Teardown(ctx); err != nil {
		allErrors = append(allErrors, fmt.Errorf("failed to teardown remote client: %w", err))
	}

	if err := c.iamOrch.Teardown(ctx); err != nil {
		allErrors = append(allErrors, fmt.Errorf("failed during IAM teardown: %w", err))
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("conductor teardown failed with %d errors: %v", len(allErrors), allErrors)
	}
	c.logger.Info().Msg("--- Conductor Teardown Complete ---")
	return nil
}
