package deployment

import (
	"cloud.google.com/go/iam/apiv1/iampb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"time"
)

func DiagnoseAndFixCloudBuildPermissions(ctx context.Context, projectID string, logger zerolog.Logger) (bool, error) {

	resourceManager, err := NewResourceManager()
	if err != nil {
		return false, err
	}
	defer resourceManager.Close()

	project, err := resourceManager.GetProject(ctx, projectID)
	if err != nil {
		return false, err
	}

	projectNumber, err := resourceManager.GetProjectNumber(ctx, projectID)
	if err != nil {
		return false, err
	}

	// --- Step 1: Identify the Cloud Build SA ---
	cloudBuildSAEmail := fmt.Sprintf("%s@cloudbuild.gserviceaccount.com", projectNumber)
	cloudBuildMember := "serviceAccount:" + cloudBuildSAEmail
	logger.Log().Str("SA", cloudBuildSAEmail).Msg("Checking permissions for default Cloud Build SA")

	// --- Step 2: Get the Full IAM Policy for the Project ---
	logger.Log().Str("projectID", projectID).Msg("Fetching full IAM policy for project")

	iamPolicy, err := resourceManager.GetProjectIAMHandle(ctx, project.Name)
	if err != nil {
		return false, err
	}

	// --- Step 3: Diagnose Existing Roles ---
	fmt.Println("IAM Policy Diagnostic Report")

	// This map now includes all roles required for the full workflow.
	requiredRoles := map[string]bool{
		"roles/cloudbuild.serviceAgent": false, // For core build operations
		"roles/storage.objectViewer":    false, // For reading source AND pulling from GCR
		"roles/artifactregistry.writer": false, // For pushing images to Artifact Registry
		"roles/run.admin":               false, // For deploying to Cloud Run
		"roles/iam.serviceAccountUser":  false, // For setting Cloud Run service identit
	}

	for _, binding := range iamPolicy.Bindings {
		// Check if the role is one we care about
		if _, ok := requiredRoles[binding.Role]; ok {
			// Check if the Cloud Build SA is a member of this role
			for _, member := range binding.Members {
				if member == cloudBuildMember {
					requiredRoles[binding.Role] = true
					break
				}
			}
		}
	}

	var missingRoles []string
	for role, found := range requiredRoles {
		if found {
			fmt.Printf("  [PRESENT ✅] %s \n", role)
		} else {
			fmt.Printf("  [MISSING ❌] %s \n", role)
			missingRoles = append(missingRoles, role)
		}
	}

	// --- Step 4: Grant Any Missing Roles ---
	if len(missingRoles) > 0 {
		fmt.Printf("\n--- Granting %d Missing Roles... --- \n", len(missingRoles))
		for _, role := range missingRoles {
			fmt.Printf("  Attempting to grant role: %s \n", role)
			err := resourceManager.AddProjectIAMBinding(ctx, projectID, cloudBuildMember, role)
			if err != nil {
				// We use t.Errorf instead of require.NoError to report all failures
				logger.Warn().Str("role", role).Err(err).Msg("Failed to grant role")
			} else {
				fmt.Printf("  Successfully granted role: %s \n", role)
			}
		}
		return true, nil

	}
	return false, nil
}

type ResourceManager struct {
	rmClient *resourcemanager.ProjectsClient
}

func NewResourceManager() (*ResourceManager, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rmClient, err := resourcemanager.NewProjectsClient(ctx)
	if err != nil {
		return nil, err
	}

	return &ResourceManager{rmClient}, nil
}

func (rm *ResourceManager) Close() error {
	return rm.rmClient.Close()
}

func (rm *ResourceManager) AddProjectIAMBinding(ctx context.Context, projectID, member, role string) error {
	projectName := fmt.Sprintf("projects/%s", projectID)

	// 1. Get the current IAM policy for the project.
	getPolicyRequest := &iampb.GetIamPolicyRequest{
		Resource: projectName,
	}
	policy, err := rm.rmClient.GetIamPolicy(ctx, getPolicyRequest)
	if err != nil {
		return fmt.Errorf("failed to get project IAM policy: %w", err)
	}

	// 2. Modify the policy in-memory.
	var bindingToModify *iampb.Binding
	for _, b := range policy.Bindings {
		if b.Role == role {
			bindingToModify = b
			break
		}
	}

	// If no binding exists for the role, create a new one.
	if bindingToModify == nil {
		bindingToModify = &iampb.Binding{
			Role:    role,
			Members: []string{},
		}
		policy.Bindings = append(policy.Bindings, bindingToModify)
	}

	// Add the member to the binding if they aren't already there.
	memberExists := false
	for _, m := range bindingToModify.Members {
		if m == member {
			memberExists = true
			break
		}
	}

	if !memberExists {
		bindingToModify.Members = append(bindingToModify.Members, member)
	} else {
		// Member already has the role, no changes are needed.
		fmt.Printf("Member '%s' already has role '%s' on project '%s'. No changes made.\n", member, role, projectID)
		return nil
	}

	// 3. Set the updated policy back on the project.
	setPolicyRequest := &iampb.SetIamPolicyRequest{
		Resource: projectName,
		Policy:   policy,
	}
	_, err = rm.rmClient.SetIamPolicy(ctx, setPolicyRequest)
	if err != nil {
		return fmt.Errorf("failed to set project IAM policy: %w", err)
	}

	fmt.Printf("Successfully granted role '%s' to member '%s' on project '%s'.\n", role, member, projectID)
	return nil
}

func (rm *ResourceManager) GetProject(ctx context.Context, projectID string) (*resourcemanagerpb.Project, error) {
	getProjectReq := &resourcemanagerpb.GetProjectRequest{
		Name: fmt.Sprintf("projects/%s", projectID),
	}
	return rm.rmClient.GetProject(ctx, getProjectReq)
}

func (rm *ResourceManager) GetProjectIAMHandle(ctx context.Context, projectName string) (*iampb.Policy, error) {
	return rm.rmClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: projectName})
}

func (rm *ResourceManager) GetProjectNumber(ctx context.Context, projectID string) (string, error) {
	getProjectReq := &resourcemanagerpb.GetProjectRequest{
		Name: fmt.Sprintf("projects/%s", projectID),
	}
	project, err := rm.rmClient.GetProject(ctx, getProjectReq)
	if err != nil {
		return "", err
	}
	projectNumber := project.Name[len("projects/"):]
	return projectNumber, nil
}
