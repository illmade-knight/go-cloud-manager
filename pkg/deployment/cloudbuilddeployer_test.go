package deployment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/illmade-knight/go-cloud-manager/pkg/servicemanager"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/run/v2"
)

// --- Mocks for Dependencies ---

type MockStorageAPI struct{ mock.Mock }

func (m *MockStorageAPI) EnsureBucketExists(ctx context.Context, bucketName string) error {
	args := m.Called(ctx, bucketName)
	return args.Error(0)
}
func (m *MockStorageAPI) Upload(ctx context.Context, sourceDir, bucket, objectName string) error {
	args := m.Called(ctx, sourceDir, bucket, objectName)
	return args.Error(0)
}

type MockArtifactRegistryAPI struct{ mock.Mock }

func (m *MockArtifactRegistryAPI) EnsureRepositoryExists(ctx context.Context, repoName, region string) error {
	args := m.Called(ctx, repoName, region)
	return args.Error(0)
}

type MockCloudBuildAPI struct{ mock.Mock }

func (m *MockCloudBuildAPI) TriggerBuildAndWait(ctx context.Context, gcsSourceObject string, spec servicemanager.DeploymentSpec) error {
	args := m.Called(ctx, gcsSourceObject, spec)
	return args.Error(0)
}

type MockCloudRunAPI struct{ mock.Mock }

func (m *MockCloudRunAPI) CreateOrUpdate(ctx context.Context, serviceName, saEmail string, spec servicemanager.DeploymentSpec) (*run.GoogleCloudRunV2Service, error) {
	args := m.Called(ctx, serviceName, saEmail, spec)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*run.GoogleCloudRunV2Service), args.Error(1)
}
func (m *MockCloudRunAPI) Delete(ctx context.Context, serviceName string) error {
	args := m.Called(ctx, serviceName)
	return args.Error(0)
}

// --- Test Cases ---

func TestCloudBuildDeployer_Deploy_Success(t *testing.T) {
	// --- Arrange ---
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	mockStorage := new(MockStorageAPI)
	mockAR := new(MockArtifactRegistryAPI)
	mockBuilder := new(MockCloudBuildAPI)
	mockRunner := new(MockCloudRunAPI)

	logger := zerolog.Nop()
	projectID := "test-project"
	region := "test-region"
	bucket := "test-bucket"
	serviceName := "test-service"
	saEmail := "test-sa@test-project.iam.gserviceaccount.com"
	spec := servicemanager.DeploymentSpec{
		SourcePath: "/tmp/source",
		ImageRepo:  "test-repo",
		Region:     region,
	}
	expectedURL := "https://test-service-xyz.a.run.app"

	deployer := NewCloudBuildDeployerForTest(projectID, region, bucket, logger, mockStorage, mockAR, mockBuilder, mockRunner)

	// Set up mock expectations in the order they should be called.
	mockStorage.On("EnsureBucketExists", ctx, bucket).Return(nil).Once()
	mockAR.On("EnsureRepositoryExists", ctx, spec.ImageRepo, spec.Region).Return(nil).Once()
	mockStorage.On("Upload", ctx, spec.SourcePath, bucket, mock.AnythingOfType("string")).Return(nil).Once()
	mockBuilder.On("TriggerBuildAndWait", ctx, mock.AnythingOfType("string"), mock.Anything).Return(nil).Once()
	mockRunner.On("CreateOrUpdate", ctx, serviceName, saEmail, mock.Anything).Return(&run.GoogleCloudRunV2Service{Uri: expectedURL}, nil).Once()

	// --- Act ---
	url, err := deployer.Deploy(ctx, serviceName, saEmail, spec)

	// --- Assert ---
	require.NoError(t, err)
	require.Equal(t, expectedURL, url)
	mockStorage.AssertExpectations(t)
	mockAR.AssertExpectations(t)
	mockBuilder.AssertExpectations(t)
	mockRunner.AssertExpectations(t)
}

func TestCloudBuildDeployer_Deploy_BuildFails(t *testing.T) {
	// --- Arrange ---
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	mockStorage := new(MockStorageAPI)
	mockAR := new(MockArtifactRegistryAPI)
	mockBuilder := new(MockCloudBuildAPI)
	mockRunner := new(MockCloudRunAPI) // Should not be called

	deployer := NewCloudBuildDeployerForTest("p", "r", "b", zerolog.Nop(), mockStorage, mockAR, mockBuilder, mockRunner)
	buildError := errors.New("buildpack failed")

	// Set up mocks, expecting the flow to stop after the build fails.
	mockStorage.On("EnsureBucketExists", ctx, mock.Anything).Return(nil)
	mockAR.On("EnsureRepositoryExists", ctx, mock.Anything, mock.Anything).Return(nil)
	mockStorage.On("Upload", ctx, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	mockBuilder.On("TriggerBuildAndWait", ctx, mock.Anything, mock.Anything).Return(buildError)

	// --- Act ---
	_, err := deployer.Deploy(ctx, "test-service", "sa@email.com", servicemanager.DeploymentSpec{SourcePath: "/src", ImageRepo: "repo", Region: "region"})

	// --- Assert ---
	require.Error(t, err)
	require.ErrorContains(t, err, "cloud Build failed")
	require.ErrorIs(t, err, buildError)
	mockRunner.AssertNotCalled(t, "CreateOrUpdate", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestCloudBuildDeployer_Teardown(t *testing.T) {
	// --- Arrange ---
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	mockRunner := new(MockCloudRunAPI)
	deployer := NewCloudBuildDeployerForTest("p", "r", "b", zerolog.Nop(), nil, nil, nil, mockRunner)
	serviceName := "service-to-delete"

	mockRunner.On("Delete", ctx, serviceName).Return(nil).Once()

	// --- Act ---
	err := deployer.Teardown(ctx, serviceName)

	// --- Assert ---
	require.NoError(t, err)
	mockRunner.AssertExpectations(t)
}
