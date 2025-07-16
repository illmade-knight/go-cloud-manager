package deployment_test

import (
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"context"
	"github.com/stretchr/testify/mock"
	"google.golang.org/api/run/v2"
	"io"
)

// --- Mocks for All Dependencies ---

type MockStorageUploader struct{ mock.Mock }

func (m *MockStorageUploader) Upload(ctx context.Context, bucket, object string, content io.Reader) error {
	args := m.Called(ctx, bucket, object, mock.Anything)
	return args.Error(0)
}

type MockCloudBuilder struct{ mock.Mock }

func (m *MockCloudBuilder) TriggerBuild(ctx context.Context, req *cloudbuildpb.CreateBuildRequest) (*longrunningpb.Operation, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(*longrunningpb.Operation), args.Error(1)
}
func (m *MockCloudBuilder) PollBuildOperation(ctx context.Context, opName string) (*cloudbuildpb.Build, error) {
	args := m.Called(ctx, opName)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*cloudbuildpb.Build), args.Error(1)
}

type MockCloudRunAPI struct{ mock.Mock }

func (m *MockCloudRunAPI) CreateOrUpdateService(ctx context.Context, parent, serviceID string, cfg *run.GoogleCloudRunV2Service) error {
	args := m.Called(ctx, parent, serviceID, cfg)
	return args.Error(0)
}

//func TestCloudBuildDeployer_Deploy(t *testing.T) {
//	// --- Arrange ---
//	mockUploader := new(MockStorageUploader)
//	mockBuilder := new(MockCloudBuilder)
//	mockRunClient := new(MockCloudRunAPI)
//
//	deployer := deployment.NewCloudBuildDeployerForTest(mockUploader, mockBuilder, mockRunClient, zerolog.Nop())
//
//	serviceName := "my-native-service"
//	imageTag := "gcr.io/proj/my-native-service:v1"
//	spec := servicemanager.DeploymentSpec{Image: imageTag}
//	saEmail := "test-sa@proj.iam.gserviceaccount.com"
//	mockOperation := &longrunningpb.Operation{Name: "operations/test-build-op-123"}
//
//	// Set up the full sequence of expected calls
//	mockUploader.On("Upload", mock.Anything, "test-source-bucket", mock.AnythingOfType("string"), mock.Anything).Return(nil).Once()
//	mockBuilder.On("TriggerBuild", mock.Anything, mock.AnythingOfType("*cloudbuildpb.CreateBuildRequest")).Return(mockOperation, nil).Once()
//	mockBuilder.On("PollBuildOperation", mock.Anything, mockOperation.Name).Return(&cloudbuildpb.Build{}, nil).Once()
//	mockRunClient.On("CreateOrUpdateService", mock.Anything, "projects/test-project/locations/us-central1", serviceName, mock.AnythingOfType("*run.GoogleCloudRunV2Service")).Return(nil).Once()
//
//	// --- Act ---
//	err := deployer.Deploy(context.Background(), serviceName, saEmail, spec)
//
//	// --- Assert ---
//	require.NoError(t, err)
//	mockUploader.AssertExpectations(t)
//	mockBuilder.AssertExpectations(t)
//	mockRunClient.AssertExpectations(t)
//}
