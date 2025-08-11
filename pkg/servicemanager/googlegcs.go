package servicemanager

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/api/option"

	"cloud.google.com/go/storage"
)

// gcsBucketHandle is an internal interface that abstracts the methods we need from
// *storage.BucketHandle. This is the key to allowing mocks to be used in testing.
// REFACTOR_NOTE: The IAM() method has been correctly removed from this interface.
type gcsBucketHandle interface {
	Attrs(ctx context.Context) (*storage.BucketAttrs, error)
	Create(ctx context.Context, projectID string, attrs *storage.BucketAttrs) error
	Update(ctx context.Context, attrs storage.BucketAttrsToUpdate) (*storage.BucketAttrs, error)
	Delete(ctx context.Context) error
}

// REFACTOR_NOTE: This new unexported struct is the bridge that makes the REAL
// *storage.BucketHandle satisfy our internal gcsBucketHandle interface.
type realGCSBucketHandle struct {
	handle *storage.BucketHandle
}

func (r *realGCSBucketHandle) Attrs(ctx context.Context) (*storage.BucketAttrs, error) {
	return r.handle.Attrs(ctx)
}
func (r *realGCSBucketHandle) Create(ctx context.Context, projectID string, attrs *storage.BucketAttrs) error {
	return r.handle.Create(ctx, projectID, attrs)
}
func (r *realGCSBucketHandle) Update(ctx context.Context, attrs storage.BucketAttrsToUpdate) (*storage.BucketAttrs, error) {
	return r.handle.Update(ctx, attrs)
}
func (r *realGCSBucketHandle) Delete(ctx context.Context) error {
	return r.handle.Delete(ctx)
}

// --- Conversion Functions (Unchanged) ---
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

// --- GCS Iterator Adapter (Unchanged) ---
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

// gcsBucketHandleAdapter now correctly holds the internal interface again, restoring testability.
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
	if err != nil && !errors.Is(err, storage.ErrBucketNotExist) {
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

// gcsClientAdapter wraps a *storage.Client to conform to our StorageClient interface.
type gcsClientAdapter struct {
	client *storage.Client
}

func (a *gcsClientAdapter) Bucket(name string) StorageBucketHandle {
	// REFACTOR_NOTE: This now correctly wraps the real handle in our two adapter layers.
	realHandle := &realGCSBucketHandle{handle: a.client.Bucket(name)}
	return &gcsBucketHandleAdapter{bucket: realHandle}
}

func (a *gcsClientAdapter) Buckets(ctx context.Context, projectID string) BucketIterator {
	return &gcsBucketIteratorAdapter{it: a.client.Buckets(ctx, projectID)}
}

func (a *gcsClientAdapter) Close() error {
	return a.client.Close()
}

// NewGCSClientAdapter creates a new StorageClient adapter from a concrete *storage.Client.
func NewGCSClientAdapter(client *storage.Client) StorageClient {
	if client == nil {
		return nil
	}
	return &gcsClientAdapter{client: client}
}

// CreateGoogleGCSClient creates a real GCS client wrapped in the StorageClient interface.
func CreateGoogleGCSClient(ctx context.Context, clientOpts ...option.ClientOption) (StorageClient, error) {
	realClient, err := storage.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("storage.NewClient: %w", err)
	}
	return NewGCSClientAdapter(realClient), nil
}
