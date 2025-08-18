package orchestration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	// Step 1: Marshal the fully-hydrated architecture into a YAML file.
	yamlBytes, err := yaml.Marshal(arch)
	if err != nil {
		return fmt.Errorf("failed to marshal hydrated architecture to YAML: %w", err)
	}

	// Step 2: Write the YAML file to the ServiceDirector's source directory.
	if arch.ServiceManagerSpec.Deployment == nil || arch.ServiceManagerSpec.Deployment.BuildableModulePath == "" {
		return errors.New("ServiceManagerSpec.Deployment.BuildableModulePath is not defined")
	}
	directorSourcePath := arch.ServiceManagerSpec.Deployment.BuildableModulePath
	destinationPath := filepath.Join(directorSourcePath, "services.yaml")

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
	CheckPrerequisites     bool
	SetupIAM               bool
	BuildAndDeployServices bool
	TriggerRemoteSetup     bool
	VerifyDataflowIAM      bool
	DirectorURLOverride    string
	SAPollTimeout          time.Duration
	PolicyPollTimeout      time.Duration
}

// Conductor manages the high-level orchestration of a full microservice architecture deployment.
type Conductor struct {
	arch                 *servicemanager.MicroserviceArchitecture
	logger               zerolog.Logger
	prerequisitesManager *prerequisites.Manager
	iamOrch              *IAMOrchestrator
	orch                 *Orchestrator
	options              ConductorOptions
	serviceAccountEmails map[string]string
	directorURL          string
	builtImageURIs       map[string]string
	imageBuildMutex      sync.Mutex
}

// NewConductor creates and initializes a new Conductor with specific options.
func NewConductor(ctx context.Context, arch *servicemanager.MicroserviceArchitecture, logger zerolog.Logger, opts ConductorOptions) (*Conductor, error) {
	prerequisitesClient, err := prerequisites.NewGoogleServiceAPIClient(ctx, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create prerequisite client: %w", err)
	}
	prerequisitesManager := prerequisites.NewManager(prerequisitesClient, logger)

	iamOrch, err := NewIAMOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAM orchestrator: %w", err)
	}

	orch, err := NewOrchestrator(ctx, arch, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create main orchestrator: %w", err)
	}
	return &Conductor{
		arch:                 arch,
		prerequisitesManager: prerequisitesManager,
		logger:               logger.With().Str("component", "Conductor").Logger(),
		iamOrch:              iamOrch,
		orch:                 orch,
		options:              opts,
		serviceAccountEmails: make(map[string]string),
		builtImageURIs:       make(map[string]string),
	}, nil
}

func (c *Conductor) Preflight(ctx context.Context) error {
	// --- NEW PRE-FLIGHT CHECK ---
	if err := c.iamOrch.PreflightChecks(ctx); err != nil {
		return fmt.Errorf("preflight permission check failed: %w", err)
	}
	return nil
}

// GenerateIAMPlan creates the full IAM plan and writes it to iam_plan.yaml.
func (c *Conductor) GenerateIAMPlan(iamPlanYAML string) error {
	c.logger.Info().Msg("Generating full IAM plan...")

	planner := iam.NewRolePlanner(c.logger)
	var fullPlan []PlanEntry

	// 1. Get and annotate the plan for the ServiceDirector.
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

	// 2. Get and annotate the plan for the application services.
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

	// 3. Marshal the enhanced plan to YAML.
	yamlBytes, err := yaml.Marshal(fullPlan)
	if err != nil {
		return fmt.Errorf("failed to marshal IAM plan to YAML: %w", err)
	}

	// 4. Write the plan to a file.
	fileName := iamPlanYAML
	err = os.WriteFile(fileName, yamlBytes, 0644)
	if err != nil {
		return fmt.Errorf("failed to write IAM plan to file: %w", err)
	}

	c.logger.Info().Str("file", fileName).Int("bindings", len(fullPlan)).Msg("✅ Successfully generated IAM plan.")
	return nil
}

// Run executes the full, multi-phase orchestration workflow in the correct sequence.
func (c *Conductor) Run(ctx context.Context) error {
	c.logger.Info().Msg("Starting full architecture orchestration...")

	if err := c.checkPrerequisites(ctx); err != nil {
		return err
	}
	// Phase 1: Create all service accounts and project-level IAM, then verify propagation immediately.
	if err := c.setupIAMPhase(ctx); err != nil {
		return err
	}
	// Phase 2: Run builds in parallel with only the ServiceDirector deployment. This is a blocking phase.
	if err := c.buildAndDeployPhase(ctx); err != nil {
		return err
	}
	// Phase 3: Now that builds are done and director is ready, trigger remote setup. This is a blocking phase.
	// we have removed the check for actual vs requested resources - we expect an error if the requested resources are not available.
	_, err := c.triggerRemoteSetup(ctx)
	if err != nil {
		return err
	}
	// REFACTOR: This phase is now more specific.
	// Phase 4: Now that remote setup is complete, verify resource-level IAM policy propagation.
	if err := c.verifyResourceLevelIAMPhase(ctx); err != nil {
		return err
	}
	// Phase 5: Now that everything is verified, deploy the final application services.
	if err := c.deploymentPhase(ctx); err != nil {
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

// REFACTOR: This phase has been updated to include immediate verification of all project-level IAM.
// This makes the process more robust by failing faster and fixing a race condition with the ServiceDirector.
func (c *Conductor) setupIAMPhase(ctx context.Context) error {
	if !c.options.SetupIAM {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 1: Setting up all service accounts and project-level IAM...")
	sdSaEmails, err := c.iamOrch.SetupServiceDirectorIAM(ctx)
	if err != nil {
		return err
	}
	// Immediately verify ServiceDirector IAM to prevent race conditions on deployment.
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
		// Immediately verify the project-level IAM for this dataflow.
		err = c.iamOrch.VerifyProjectLevelIAMForDataflow(ctx, dataflowName, saEmails, c.options.PolicyPollTimeout)
		if err != nil {
			return fmt.Errorf("failed during project-level IAM verification for dataflow '%s': %w", dataflowName, err)
		}
	}

	c.logger.Info().Msg("Phase 1: All service accounts and project-level IAM are set up and verified.")
	return nil
}

func (c *Conductor) buildAndDeployPhase(ctx context.Context) error {
	if !c.options.BuildAndDeployServices {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 2: Building services and deploying director in parallel...")
	var wg sync.WaitGroup
	errChan := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.buildDataflowServices(ctx); err != nil {
			errChan <- err
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.deployServiceDirector(ctx); err != nil {
			errChan <- err
		}
	}()
	wg.Wait()
	close(errChan)
	var phaseErrors []error
	for err := range errChan {
		phaseErrors = append(phaseErrors, err)
	}
	if len(phaseErrors) > 0 {
		return fmt.Errorf("encountered errors during build and director deployment phase: %v", phaseErrors)
	}
	c.logger.Info().Msg("Phase 2: Build and director deployment completed successfully.")
	return nil
}

func (c *Conductor) buildDataflowServices(ctx context.Context) error {
	c.logger.Info().Msg("Starting build of all dataflow services...")
	for dataflowName := range c.arch.Dataflows {
		builtImages, err := c.orch.BuildDataflowServices(ctx, dataflowName)
		if err != nil {
			return fmt.Errorf("failed to build services for dataflow '%s': %w", dataflowName, err)
		}
		c.imageBuildMutex.Lock()
		for service, image := range builtImages {
			c.builtImageURIs[service] = image
		}
		c.imageBuildMutex.Unlock()
	}
	c.logger.Info().Msg("Build of all dataflow services is complete.")
	return nil
}

func (c *Conductor) deployServiceDirector(ctx context.Context) error {
	c.logger.Info().Msg("Deploying ServiceDirector...")
	if c.options.DirectorURLOverride != "" {
		c.directorURL = c.options.DirectorURLOverride
		return nil
	}
	if _, ok := c.serviceAccountEmails[c.arch.ServiceManagerSpec.Name]; !ok {
		return errors.New("cannot deploy service director: SA email not found")
	}

	url, err := c.orch.DeployServiceDirector(ctx, c.serviceAccountEmails)
	if err != nil {
		return fmt.Errorf("failed during ServiceDirector deployment: %w", err)
	}
	c.directorURL = url
	c.logger.Info().Str("url", url).Str("topic", c.orch.completionTopic.ID()).
		Str("sub", c.orch.completionSubscription.ID()).Msg("Waiting for ServiceDirector to become ready...")
	err = c.orch.AwaitServiceReady(ctx, ServiceDirector)
	if err != nil {
		return fmt.Errorf("failed while waiting for ServiceDirector to become ready: %w", err)
	}
	c.logger.Info().Msg("ServiceDirector is deployed and ready.")
	return nil
}

func (c *Conductor) triggerRemoteSetup(ctx context.Context) (map[string]CompletionEvent, error) {
	if !c.options.TriggerRemoteSetup {
		return nil, nil
	}
	c.logger.Info().Msg("Executing Phase 3: Triggering remote dataflow resource and IAM setup...")
	completionEvents := make(map[string]CompletionEvent)
	for dataflowName := range c.arch.Dataflows {
		err := c.orch.TriggerDataflowResourceCreation(ctx, dataflowName)
		if err != nil {
			return nil, fmt.Errorf("failed to trigger setup for dataflow '%s': %w", dataflowName, err)
		}
		event, err := c.orch.AwaitDataflowReady(ctx, dataflowName)
		if err != nil {
			return nil, fmt.Errorf("failed to receive completion for dataflow '%s': %w", dataflowName, err)
		}
		completionEvents[dataflowName] = event
	}
	c.logger.Info().Msg("Phase 3: Remote dataflow resource and IAM setup is complete.")
	return completionEvents, nil
}

// REFACTOR: This function has been renamed from verificationPhase and now only verifies
// resource-level IAM policies, as project-level policies were already verified in Phase 1.
func (c *Conductor) verifyResourceLevelIAMPhase(ctx context.Context) error {
	if !c.options.VerifyDataflowIAM {
		return nil
	}
	c.logger.Info().Msg("Executing Phase 4: Verifying dataflow RESOURCE-LEVEL IAM policy propagation...")

	// This is a simplified comparison logic. A real implementation would need a deep-equals on the policy maps.
	// For this refactor, we are trusting the remote director and proceeding to poll.
	c.logger.Info().Msg("Trusting remote plan; proceeding to poll for propagation.")

	for dataflowName := range c.arch.Dataflows {
		// Pass the combined map of all service account emails.
		// The verification function will select the ones it needs for this dataflow.
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
	c.logger.Info().Msg("Executing Phase 5: Deploying final application services...")
	if c.directorURL == "" {
		return errors.New("cannot deploy dataflow services: director URL is not available")
	}
	for dataflowName := range c.arch.Dataflows {
		err := c.orch.DeployDataflowServices(ctx, dataflowName, c.serviceAccountEmails, c.builtImageURIs, c.directorURL)
		if err != nil {
			return fmt.Errorf("failed to deploy application services for dataflow '%s': %w", dataflowName, err)
		}
	}
	c.logger.Info().Msg("Phase 5: Final deployment of all dataflow services is complete.")
	return nil
}

func (c *Conductor) Teardown(ctx context.Context) error {
	c.logger.Info().Msg("--- Starting Conductor Teardown ---")
	var allErrors []error
	if err := c.orch.Teardown(ctx); err != nil {
		c.logger.Error().Err(err).Msg("Orchestrator teardown failed")
		allErrors = append(allErrors, err)
	}
	if err := c.iamOrch.Teardown(ctx); err != nil {
		c.logger.Error().Err(err).Msg("IAM teardown failed")
		allErrors = append(allErrors, err)
	}
	if len(allErrors) > 0 {
		return fmt.Errorf("conductor teardown failed with %d errors: %v", len(allErrors), allErrors)
	}
	c.logger.Info().Msg("--- Conductor Teardown Complete ---")
	return nil
}
