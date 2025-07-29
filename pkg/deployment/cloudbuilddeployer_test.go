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

func (m *MockStorageUploader) Upload(ctx context.Context, bucket, object string, _ io.Reader) error {
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
