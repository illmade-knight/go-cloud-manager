//go:build cloud_integration

package deployment_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestStandardBuilderPull attempts to run a minimal Cloud Build job using a
// standard Google Cloud builder. Its only purpose is to verify that the
// build environment can successfully pull a common builder image from gcr.io.
//
//   - If this test PASSES, it proves the environment and permissions are correct for
//     pulling standard images, strongly suggesting the issue is specific to the
//     'gcr.io/k8s-build-infra/pack' builder.
//   - If this test FAILS with a "permission denied" error, it confirms there is a
//     fundamental networking or policy issue blocking access to gcr.io.
func TestStandardBuilderPull(t *testing.T) {
	// --- Setup ---
	projectID := CheckGCPAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	buildClient, err := cloudbuild.NewClient(ctx)
	require.NoError(t, err, "Failed to create Cloud Build client")
	t.Cleanup(func() { _ = buildClient.Close() })

	// --- Define a Minimal Build Request ---
	// This build has no source code. It only tries to pull a standard
	// builder and run a simple command.
	req := &cloudbuildpb.CreateBuildRequest{
		ProjectId: projectID,
		Build: &cloudbuildpb.Build{
			// We leave ServiceAccount blank to use the default Cloud Build SA
			Steps: []*cloudbuildpb.BuildStep{
				{
					Name: "gcr.io/cloud-builders/gcloud",
					Args: []string{"--version"},
				},
			},
			Options: &cloudbuildpb.BuildOptions{
				Logging: cloudbuildpb.BuildOptions_CLOUD_LOGGING_ONLY,
			},
		},
	}

	// --- Act & Assert ---
	t.Logf("Triggering minimal build on project '%s' to test builder pull...", projectID)
	op, err := buildClient.CreateBuild(ctx, req)
	require.NoError(t, err, "Failed to create the diagnostic build")

	// Wait for the build to complete.
	resp, err := op.Wait(ctx)
	require.NoError(t, err, "The diagnostic build operation failed while waiting")

	// Check the final status of the build.
	finalStatus := resp.GetStatus()
	t.Logf("Diagnostic build completed with final status: %s", finalStatus)
	require.Equal(t, cloudbuildpb.Build_SUCCESS, finalStatus, "The diagnostic build did not succeed. Check the build logs in the GCP console for details.")

	t.Log("✅ Diagnostic build SUCCEEDED. The environment can pull standard builder images.")
}

// TestFinalBuildRecipe validates the entire build process using the
// known-good, two-step build configuration discovered from 'gcloud run deploy'.
func TestFinalBuildRecipe(t *testing.T) {
	// --- Setup ---
	projectID := CheckGCPAuth(t)
	region := "us-central1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	buildClient, err := cloudbuild.NewClient(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = buildClient.Close() })
	storageClient, err := storage.NewClient(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storageClient.Close() })

	// --- 1. Prepare and Upload Source Code ---
	t.Log("Preparing and uploading source code...")
	sourceDir, cleanupSourceDir := createTestSourceDir(t, helloWorldSource)
	t.Cleanup(cleanupSourceDir)

	sourceBucketName := fmt.Sprintf("%s_cloudbuild", projectID)
	sourceObject := fmt.Sprintf("source/final-diagnostic-test-%d.tar.gz", time.Now().Unix())

	err = uploadDirToGCS(ctx, storageClient, sourceDir, sourceBucketName, sourceObject)
	require.NoError(t, err)
	t.Logf("Source uploaded to gs://%s/%s", sourceBucketName, sourceObject)

	t.Cleanup(func() {
		t.Logf("Cleaning up GCS object: %s", sourceObject)
		err := storageClient.Bucket(sourceBucketName).Object(sourceObject).Delete(context.Background())
		if err != nil && err != storage.ErrObjectNotExist {
			t.Errorf("Failed to delete GCS source object: %v", err)
		}
	})

	// --- 2. Define and Trigger the Build ---
	outputImageTag := fmt.Sprintf("%s-docker.pkg.dev/%s/test-images/final-diagnostic-test:%d", region, projectID, time.Now().Unix())
	gcsSourceUri := fmt.Sprintf("gs://%s/%s", sourceBucketName, sourceObject)
	buildId := uuid.New().String()

	mainBuildCommand := fmt.Sprintf(
		`trap "[ -f /workspace/.google-builder-output/output ] && mv /workspace/.google-builder-output/output $$BUILDER_OUTPUT" EXIT ; pack build %s --network cloudbuild --volume /workspace:/builder-output/:rw`,
		outputImageTag,
	)

	req := &cloudbuildpb.CreateBuildRequest{
		ProjectId: projectID,
		Build: &cloudbuildpb.Build{
			Source: &cloudbuildpb.Source{
				Source: &cloudbuildpb.Source_StorageSource{
					StorageSource: &cloudbuildpb.StorageSource{
						Bucket: sourceBucketName,
						Object: sourceObject,
					},
				},
			},
			Steps: []*cloudbuildpb.BuildStep{
				{
					Name:       "gcr.io/k8s-skaffold/pack",
					Id:         "pre-buildpack",
					Entrypoint: "/bin/sh",
					Args:       []string{"-c", "chmod a+w /workspace && pack config default-builder gcr.io/buildpacks/builder:latest && pack config trusted-builders add gcr.io/buildpacks/builder:latest"},
				},
				{
					Name:       "gcr.io/k8s-skaffold/pack",
					Id:         "build",
					Entrypoint: "/bin/sh",
					// Add all the GOOGLE_* env vars that gcloud uses as hints for the buildpack
					Env: []string{
						"GOOGLE_LABEL_RUN_IMAGE=gcr.io/buildpacks/google-22/run",
						"GOOGLE_LABEL_SOURCE=" + gcsSourceUri,
						"GOOGLE_RUNTIME_IMAGE_REGION=" + region,
						"GOOGLE_LABEL_BUILD_ID=" + buildId,
						"GOOGLE_LABEL_BASE_IMAGE=gcr.io/buildpacks/google-22/run",
					},
					Args: []string{"-c", mainBuildCommand},
				},
			},
			Images: []string{outputImageTag},
		},
	}

	// --- 3. Act & Assert ---
	t.Logf("Triggering final diagnostic build for image: %s", outputImageTag)
	op, err := buildClient.CreateBuild(ctx, req)
	require.NoError(t, err, "Failed to create the diagnostic build")

	resp, err := op.Wait(ctx)
	require.NoError(t, err, "The diagnostic build operation failed while waiting")

	finalStatus := resp.GetStatus()
	t.Logf("Diagnostic build completed with final status: %s", finalStatus)
	require.Equal(t, cloudbuildpb.Build_SUCCESS, finalStatus, "The final diagnostic build did not succeed.")

	t.Log("✅ Diagnostic build SUCCEEDED. The build recipe is correct.")
}

// Helper function to upload a directory to GCS as a tar.gz archive.
func uploadDirToGCS(ctx context.Context, client *storage.Client, sourceDir, bucket, objectName string) error {
	buf := new(bytes.Buffer)
	gzipWriter := gzip.NewWriter(buf)
	tarWriter := tar.NewWriter(gzipWriter)

	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		header.Name, err = filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() {
			_ = file.Close()
		}()
		_, err = io.Copy(tarWriter, file)
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to walk source directory '%s': %w", sourceDir, err)
	}
	_ = tarWriter.Close()

	_ = gzipWriter.Close()

	w := client.Bucket(bucket).Object(objectName).NewWriter(ctx)
	if _, err = io.Copy(w, buf); err != nil {
		_ = w.Close() // Teardown writer on error
		return fmt.Errorf("failed to copy source to GCS: %w", err)
	}
	return w.Close()
}
