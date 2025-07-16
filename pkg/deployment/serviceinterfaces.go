package deployment

import (
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"context"
	"io"
)

type IStorageUploader interface {
	Upload(ctx context.Context, bucket, objectName string, content io.Reader) error
}

// ICloudBuilder defines a contract for triggering and monitoring a Cloud Build job.
type ICloudBuilder interface {
	TriggerBuild(ctx context.Context, req *cloudbuildpb.CreateBuildRequest) (*longrunningpb.Operation, error)
	PollBuildOperation(ctx context.Context, opName string) (*cloudbuildpb.Build, error)
}
