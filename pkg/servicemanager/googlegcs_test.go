package servicemanager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"cloud.google.com/go/iam"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"google.golang.org/api/iterator"
)

// --- Mocks ---

// mockGCSBucketHandle is a mock that simulates the behavior of a GCS bucket handle.
// It implements the `gcsBucketHandle` interface, allowing it to be used by gcsBucketHandleAdapter in tests.
type mockGCSBucketHandle struct {
	attrsFn  func(ctx context.Context) (*storage.BucketAttrs, error)
	createFn func(ctx context.Context, projectID string, attrs *storage.BucketAttrs) error
	updateFn func(ctx context.Context, attrs storage.BucketAttrsToUpdate) (*storage.BucketAttrs, error)
	deleteFn func(ctx context.Context) error
	iamFn    func() *iam.Handle
}

// The following methods implement the gcsBucketHandle interface for the mock.

func (m *mockGCSBucketHandle) Attrs(ctx context.Context) (*storage.BucketAttrs, error) {
	if m.attrsFn != nil {
		return m.attrsFn(ctx)
	}
	return nil, errors.New("Attrs not implemented in mock")
}

func (m *mockGCSBucketHandle) Create(ctx context.Context, projectID string, attrs *storage.BucketAttrs) error {
	if m.createFn != nil {
		return m.createFn(ctx, projectID, attrs)
	}
	return errors.New("Create not implemented in mock")
}

func (m *mockGCSBucketHandle) Update(ctx context.Context, attrs storage.BucketAttrsToUpdate) (*storage.BucketAttrs, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, attrs)
	}
	return nil, errors.New("Update not implemented in mock")
}

func (m *mockGCSBucketHandle) Delete(ctx context.Context) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx)
	}
	return errors.New("Delete not implemented in mock")
}

func (m *mockGCSBucketHandle) IAM() *iam.Handle {
	if m.iamFn != nil {
		return m.iamFn()
	}
	return nil
}

// mockBucketIterator is a mock implementation of the BucketIterator for testing.
type mockBucketIterator struct {
	buckets []*storage.BucketAttrs
	nextIdx int
}

func (m *mockBucketIterator) Next() (*storage.BucketAttrs, error) {
	if m.nextIdx >= len(m.buckets) {
		return nil, iterator.Done
	}
	bucket := m.buckets[m.nextIdx]
	m.nextIdx++
	return bucket, nil
}

// TestFromGCSBucketAttrs tests the conversion from GCS-specific attributes to generic ones.
func TestFromGCSBucketAttrs(t *testing.T) {
	gcsAttrs := &storage.BucketAttrs{
		Name:              "test-bucket",
		Location:          "US",
		StorageClass:      "STANDARD",
		VersioningEnabled: true,
		Labels:            map[string]string{"env": "test"},
		Lifecycle: storage.Lifecycle{
			Rules: []storage.LifecycleRule{
				{
					Action:    storage.LifecycleAction{Type: "Delete"},
					Condition: storage.LifecycleCondition{AgeInDays: 30},
				},
			},
		},
	}

	expected := &BucketAttributes{
		Name:              "test-bucket",
		Location:          "US",
		StorageClass:      "STANDARD",
		VersioningEnabled: true,
		Labels:            map[string]string{"env": "test"},
		LifecycleRules: []LifecycleRule{
			{
				Action:    LifecycleAction{Type: "Delete"},
				Condition: LifecycleCondition{AgeInDays: 30},
			},
		},
	}

	actual := fromGCSBucketAttrs(gcsAttrs)

	assert.True(t, reflect.DeepEqual(expected, actual), "fromGCSBucketAttrs conversion was incorrect")
}

// TestToGCSBucketAttrs tests the conversion from generic attributes to GCS-specific ones.
func TestToGCSBucketAttrs(t *testing.T) {
	attrs := &BucketAttributes{
		Name:              "test-bucket",
		Location:          "US",
		StorageClass:      "STANDARD",
		VersioningEnabled: true,
		Labels:            map[string]string{"env": "test"},
		LifecycleRules: []LifecycleRule{
			{
				Action:    LifecycleAction{Type: "Delete"},
				Condition: LifecycleCondition{AgeInDays: 30},
			},
		},
	}

	expected := &storage.BucketAttrs{
		Name:              "test-bucket",
		Location:          "US",
		StorageClass:      "STANDARD",
		VersioningEnabled: true,
		Labels:            map[string]string{"env": "test"},
		Lifecycle: storage.Lifecycle{
			Rules: []storage.LifecycleRule{
				{
					Action:    storage.LifecycleAction{Type: "Delete"},
					Condition: storage.LifecycleCondition{AgeInDays: 30},
				},
			},
		},
	}

	actual := toGCSBucketAttrs(attrs)

	assert.True(t, reflect.DeepEqual(expected, actual), "toGCSBucketAttrs conversion was incorrect")
}

// TestGCSBucketHandleAdapter_Update verifies the adapter's update logic.
func TestGCSBucketHandleAdapter_Update(t *testing.T) {
	ctx := context.Background()
	var capturedUpdateAttrs storage.BucketAttrsToUpdate
	updateCalled := false

	// Mock the underlying GCS bucket handle
	mockHandle := &mockGCSBucketHandle{
		attrsFn: func(ctx context.Context) (*storage.BucketAttrs, error) {
			// Return existing attributes for the label diff logic
			return &storage.BucketAttrs{Labels: map[string]string{"existing": "true", "env": "dev"}}, nil
		},
		updateFn: func(ctx context.Context, attrs storage.BucketAttrsToUpdate) (*storage.BucketAttrs, error) {
			// Capture the attributes passed to the mock's Update method
			capturedUpdateAttrs = attrs
			updateCalled = true
			// Return a mock response reflecting the update
			return &storage.BucketAttrs{Name: "test-bucket", VersioningEnabled: true}, nil
		},
	}

	// Create the adapter with the mocked handle. This is now valid.
	adapter := &gcsBucketHandleAdapter{bucket: mockHandle}

	// Define the generic update request
	storageClass := "NEARLINE"
	updateRequest := BucketAttributesToUpdate{
		VersioningEnabled: false,
		StorageClass:      &storageClass,
		Labels:            map[string]string{"new": "label", "env": "prod"},
	}

	// Call the adapter's Update method
	res, err := adapter.Update(ctx, updateRequest)
	assert.NoError(t, err)

	// Assert that the mock's Update function was actually called
	assert.True(t, updateCalled, "Expected the underlying bucket handle's Update method to be called")

	// Assert that the captured attributes have the correct non-label values.
	assert.Equal(t, "NEARLINE", capturedUpdateAttrs.StorageClass)

	// Assert that the response from the adapter is correctly converted back
	assert.NotNil(t, res)
	assert.True(t, res.VersioningEnabled)
}
