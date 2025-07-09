package servicemanager

import (
	"context"
	"fmt"
	"google.golang.org/api/option"

	"cloud.google.com/go/iam"
	"cloud.google.com/go/storage"
)

// gcsBucketHandle is an interface that abstracts the methods from *storage.BucketHandle
// that our adapter uses. This is the key to allowing mocks to be used in testing.
type gcsBucketHandle interface {
	Attrs(ctx context.Context) (*storage.BucketAttrs, error)
	Create(ctx context.Context, projectID string, attrs *storage.BucketAttrs) error
	Update(ctx context.Context, attrs storage.BucketAttrsToUpdate) (*storage.BucketAttrs, error)
	Delete(ctx context.Context) error
	IAM() *iam.Handle
}

// --- Conversion Functions ---

// fromGCSBucketAttrs converts GCS-specific bucket attributes to our generic BucketAttributes struct.
func fromGCSBucketAttrs(gcsAttrs *storage.BucketAttrs) *BucketAttributes {
	if gcsAttrs == nil {
		return nil
	}
	attrs := &BucketAttributes{
		Name:              gcsAttrs.Name,
		Location:          gcsAttrs.Location,
		StorageClass:      gcsAttrs.StorageClass,
		VersioningEnabled: gcsAttrs.VersioningEnabled,
		Labels:            gcsAttrs.Labels,
	}
	if gcsAttrs.Lifecycle.Rules != nil {
		attrs.LifecycleRules = make([]LifecycleRule, 0, len(gcsAttrs.Lifecycle.Rules))
		for _, gcsRule := range gcsAttrs.Lifecycle.Rules {
			attrs.LifecycleRules = append(attrs.LifecycleRules, LifecycleRule{
				Action:    LifecycleAction{Type: gcsRule.Action.Type},
				Condition: LifecycleCondition{AgeInDays: int(gcsRule.Condition.AgeInDays)},
			})
		}
	}
	return attrs
}

// toGCSBucketAttrs converts our generic BucketAttributes struct to a GCS-specific one for creation.
func toGCSBucketAttrs(attrs *BucketAttributes) *storage.BucketAttrs {
	if attrs == nil {
		return nil
	}
	gcsAttrs := &storage.BucketAttrs{
		Name:              attrs.Name,
		Location:          attrs.Location,
		StorageClass:      attrs.StorageClass,
		VersioningEnabled: attrs.VersioningEnabled,
		Labels:            attrs.Labels,
	}
	if attrs.LifecycleRules != nil {
		gcsLifecycle := storage.Lifecycle{}
		gcsLifecycle.Rules = make([]storage.LifecycleRule, 0, len(attrs.LifecycleRules))
		for _, rule := range attrs.LifecycleRules {
			gcsLifecycle.Rules = append(gcsLifecycle.Rules, storage.LifecycleRule{
				Action:    storage.LifecycleAction{Type: rule.Action.Type},
				Condition: storage.LifecycleCondition{AgeInDays: int64(rule.Condition.AgeInDays)},
			})
		}
		gcsAttrs.Lifecycle = gcsLifecycle
	}
	return gcsAttrs
}

// toGCSBucketAttrsToUpdate converts our generic update struct to a GCS-specific one.
func toGCSBucketAttrsToUpdate(attrs BucketAttributesToUpdate, existingGCSAttrs *storage.BucketAttrs) storage.BucketAttrsToUpdate {
	gcsUpdate := storage.BucketAttrsToUpdate{}

	if attrs.StorageClass != nil {
		gcsUpdate.StorageClass = *attrs.StorageClass
	}

	if attrs.Labels != nil {
		for k, v := range attrs.Labels {
			gcsUpdate.SetLabel(k, v)
		}
		if existingGCSAttrs != nil && existingGCSAttrs.Labels != nil {
			for k := range existingGCSAttrs.Labels {
				if _, existsInNewConfig := attrs.Labels[k]; !existsInNewConfig {
					gcsUpdate.DeleteLabel(k)
				}
			}
		}
	}
	if attrs.LifecycleRules != nil {
		gcsLifecycle := storage.Lifecycle{}
		if *attrs.LifecycleRules != nil {
			gcsLifecycle.Rules = make([]storage.LifecycleRule, 0, len(*attrs.LifecycleRules))
			for _, rule := range *attrs.LifecycleRules {
				gcsLifecycle.Rules = append(gcsLifecycle.Rules, storage.LifecycleRule{
					Action:    storage.LifecycleAction{Type: rule.Action.Type},
					Condition: storage.LifecycleCondition{AgeInDays: int64(rule.Condition.AgeInDays)},
				})
			}
		}
		gcsUpdate.Lifecycle = &gcsLifecycle
	}
	return gcsUpdate
}

// --- GCS Iterator Adapter ---

type gcsBucketIteratorAdapter struct {
	it *storage.BucketIterator
}

func (a *gcsBucketIteratorAdapter) Next() (*BucketAttributes, error) {
	gcsAttrs, err := a.it.Next()
	if err != nil {
		return nil, err
	}
	return fromGCSBucketAttrs(gcsAttrs), nil
}

// --- GCS Handle/Client Adapters ---

// gcsBucketHandleAdapter wraps a GCS bucket handle (real or mock) to conform to our StorageBucketHandle interface.
type gcsBucketHandleAdapter struct {
	bucket gcsBucketHandle
}

func (a *gcsBucketHandleAdapter) Attrs(ctx context.Context) (*BucketAttributes, error) {
	gcsAttrs, err := a.bucket.Attrs(ctx)
	if err != nil {
		return nil, err
	}
	return fromGCSBucketAttrs(gcsAttrs), nil
}

func (a *gcsBucketHandleAdapter) Create(ctx context.Context, projectID string, attrs *BucketAttributes) error {
	gcsAttrs := toGCSBucketAttrs(attrs)
	return a.bucket.Create(ctx, projectID, gcsAttrs)
}

func (a *gcsBucketHandleAdapter) Update(ctx context.Context, attrs BucketAttributesToUpdate) (*BucketAttributes, error) {
	existingGCSAttrs, err := a.bucket.Attrs(ctx)
	if err != nil && err != storage.ErrBucketNotExist {
		return nil, fmt.Errorf("failed to get existing attributes before update: %w", err)
	}

	gcsAttrsToUpdate := toGCSBucketAttrsToUpdate(attrs, existingGCSAttrs)
	updatedGCSAttrs, err := a.bucket.Update(ctx, gcsAttrsToUpdate)
	if err != nil {
		return nil, err
	}
	return fromGCSBucketAttrs(updatedGCSAttrs), nil
}

func (a *gcsBucketHandleAdapter) Delete(ctx context.Context) error {
	return a.bucket.Delete(ctx)
}

func (a *gcsBucketHandleAdapter) IAM() *iam.Handle {
	return a.bucket.IAM()
}

type gcsClientAdapter struct {
	client *storage.Client
}

func (a *gcsClientAdapter) Bucket(name string) StorageBucketHandle {
	return &gcsBucketHandleAdapter{bucket: a.client.Bucket(name)}
}

func (a *gcsClientAdapter) Buckets(ctx context.Context, projectID string) BucketIterator {
	return &gcsBucketIteratorAdapter{it: a.client.Buckets(ctx, projectID)}
}

func (a *gcsClientAdapter) Close() error {
	return a.client.Close()
}

func NewGCSClientAdapter(client *storage.Client) StorageClient {
	if client == nil {
		return nil
	}
	return &gcsClientAdapter{client: client}
}

func CreateGoogleGCSClient(ctx context.Context, clientOpts ...option.ClientOption) (StorageClient, error) {
	realClient, err := storage.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("storage.NewClient: %w", err)
	}
	return NewGCSClientAdapter(realClient), nil
}
