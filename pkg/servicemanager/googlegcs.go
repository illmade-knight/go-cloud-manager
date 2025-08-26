package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// gcsBucketHandle is an internal interface abstracting the `*storage.BucketHandle`
// methods required by this package. It is used to enable mocking for unit tests.
type gcsBucketHandle interface {
	Attrs(ctx context.Context) (*storage.BucketAttrs, error)
	Create(ctx context.Context, projectID string, attrs *storage.BucketAttrs) error
	Update(ctx context.Context, attrs storage.BucketAttrsToUpdate) (*storage.BucketAttrs, error)
	Delete(ctx context.Context) error
}

// realGCSBucketHandle adapts a real `*storage.BucketHandle` to satisfy the
// internal `gcsBucketHandle` interface. This struct acts as a bridge between the
// concrete Google Cloud client and our internal, testable interfaces.
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

// isGCSBucketNotExist checks for various forms of "not found" errors from the GCS client.
// This helper isolates the Google-specific error checking to this implementation file.
func isGCSBucketNotExist(err error) bool {
	if errors.Is(err, storage.ErrBucketNotExist) {
		return true
	}
	var gapiErr *googleapi.Error
	if errors.As(err, &gapiErr) {
		if gapiErr.Code == http.StatusNotFound {
			return true
		}
	}
	// Fallback check for the common error string.
	if err != nil && strings.Contains(err.Error(), "storage: bucket doesn't exist") {
		return true
	}
	return false
}

// The following functions translate between the generic `servicemanager` bucket
// attribute types and the GCS-specific types from the `cloud.google.com/go/storage` package.

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

	if attrs.VersioningEnabled != false {
		gcsUpdate.VersioningEnabled = attrs.VersioningEnabled
	}
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

// gcsBucketIteratorAdapter adapts a `*storage.BucketIterator` to conform to the
// package's generic `BucketIterator` interface.
type gcsBucketIteratorAdapter struct {
	it *storage.BucketIterator
}

func (a *gcsBucketIteratorAdapter) Next() (*BucketAttributes, error) {
	gcsAttrs, err := a.it.Next()
	if err != nil {
		// Note: The iterator library has its own `iterator.Done` error which is already generic.
		return nil, err
	}
	return fromGCSBucketAttrs(gcsAttrs), nil
}

// gcsBucketHandleAdapter adapts a `gcsBucketHandle` (our internal, mockable
// interface) to the public `StorageBucketHandle` interface used by the StorageManager.
type gcsBucketHandleAdapter struct {
	bucket gcsBucketHandle
}

func (a *gcsBucketHandleAdapter) Attrs(ctx context.Context) (*BucketAttributes, error) {
	gcsAttrs, err := a.bucket.Attrs(ctx)
	if isGCSBucketNotExist(err) {
		return nil, ErrBucketNotExist
	}
	if err != nil {
		return nil, err
	}
	return fromGCSBucketAttrs(gcsAttrs), nil
}

func (a *gcsBucketHandleAdapter) Create(ctx context.Context, projectID string, attrs *BucketAttributes) error {
	gcsAttrs := toGCSBucketAttrs(attrs)
	// Create generally returns a 409 Conflict if it exists, which is not a "not found" error.
	// No error translation is needed here.
	return a.bucket.Create(ctx, projectID, gcsAttrs)
}

func (a *gcsBucketHandleAdapter) Update(ctx context.Context, attrs BucketAttributesToUpdate) (*BucketAttributes, error) {
	// The manager's logic calls Attrs before Update, so we primarily need to
	// check for "not found" on the initial attribute fetch.
	existingGCSAttrs, err := a.bucket.Attrs(ctx)
	if err != nil && !isGCSBucketNotExist(err) {
		return nil, fmt.Errorf("failed to get existing attributes before update: %w", err)
	}

	gcsAttrsToUpdate := toGCSBucketAttrsToUpdate(attrs, existingGCSAttrs)
	updatedGCSAttrs, err := a.bucket.Update(ctx, gcsAttrsToUpdate)
	if isGCSBucketNotExist(err) {
		return nil, ErrBucketNotExist
	}
	if err != nil {
		return nil, err
	}
	return fromGCSBucketAttrs(updatedGCSAttrs), nil
}

func (a *gcsBucketHandleAdapter) Delete(ctx context.Context) error {
	err := a.bucket.Delete(ctx)
	if isGCSBucketNotExist(err) {
		// Translate to the generic error, which the manager interprets as "already deleted".
		return ErrBucketNotExist
	}
	return err
}

// gcsClientAdapter is an adapter that wraps the official Google Cloud Storage
// client (`*storage.Client`) to conform to the generic `StorageClient` interface
// required by the StorageManager.
type gcsClientAdapter struct {
	client *storage.Client
}

func (a *gcsClientAdapter) Bucket(name string) StorageBucketHandle {
	realHandle := &realGCSBucketHandle{handle: a.client.Bucket(name)}
	return &gcsBucketHandleAdapter{bucket: realHandle}
}

func (a *gcsClientAdapter) Buckets(ctx context.Context, projectID string) BucketIterator {
	return &gcsBucketIteratorAdapter{it: a.client.Buckets(ctx, projectID)}
}

func (a *gcsClientAdapter) Close() error {
	return a.client.Close()
}

// NewGCSClientAdapter creates a new `StorageClient` adapter from a concrete
// `*storage.Client`. This is the primary way to wrap a real GCS client for use
// with the StorageManager.
func NewGCSClientAdapter(client *storage.Client) StorageClient {
	if client == nil {
		return nil
	}
	return &gcsClientAdapter{client: client}
}

// CreateGoogleGCSClient creates a new, production-ready GCS client that conforms
// to the `StorageClient` interface. It handles the initialization of the
// official Google Cloud client and wraps it in the necessary adapter.
func CreateGoogleGCSClient(ctx context.Context, clientOpts ...option.ClientOption) (StorageClient, error) {
	realClient, err := storage.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("storage.NewClient: %w", err)
	}
	return NewGCSClientAdapter(realClient), nil
}
