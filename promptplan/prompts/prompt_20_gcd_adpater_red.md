# **Prompt 20: Integration Tests for GCS Adapter**

**Objective:** Generate the complete integration test file for the googlegcs.go adapter. This test will validate the adapter's interaction with the Google Cloud Storage API using an emulator.

**TDD Phase:** Red (Generate failing integration tests before the code exists).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the servicemanager/googlegcs\_integration\_test.go file.

Write a complete integration test that validates the full lifecycle (Create, Update, Verify, Delete) of a GCS bucket using the StorageClient interface. The test **must not** use mocks; it should be written to connect to a GCS emulator.

**Key Requirements to Test:**

* **Resource Lifecycle:** The test must create a bucket, verify its initial attributes, update its attributes, verify the changes, and finally delete it.
* **Type Conversion:** The test implicitly validates that the conversion between the generic BucketAttributes and the GCS-specific storage.BucketAttrs is correct.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in writing robust integration tests for Google Cloud services using official emulators.

**Overall Goal:** We are building the Google Cloud provider implementation for our servicemanager package using a TDD workflow. We need to write the integration tests that will specify and validate the behavior of our googlegcs.go adapter.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/googlegcs\_integration\_test.go**. This file will contain a test that uses a GCS emulator to validate the gcsClientAdapter.

**Key Requirements to Fulfill:**

* **Use Emulator:** The test setup must include logic to programmatically start and configure a GCS emulator. It should then create a real storage.Client configured to connect to this emulator.
* **Test Workflow:** The test **must** follow this sequence:
    1. **Create:** Call the adapter's Create method to create a new bucket with initial attributes (e.g., STANDARD storage class).
    2. **Verify Create:** Use the real GCS client to fetch the bucket's attributes directly from the emulator and assert that they match the initial configuration.
    3. **Update:** Call the adapter's Update method to change the bucket's attributes (e.g., to NEARLINE storage class and enable versioning).
    4. **Verify Update:** Fetch the attributes again and assert that they reflect the changes.
    5. **Delete:** Call the adapter's Delete method.
    6. **Verify Delete:** Attempt to fetch the bucket's attributes again and assert that the call fails with a "not found" error.

**Dependencies (Full source code to be provided):**

1. servicemanager/storageadapter.go (contains the StorageClient interface that the adapter will implement)
2. servicemanager/systemarchitecture.go (contains the GCSBucket struct)

Output Instructions:  
Generate the complete, well-commented Go code for the googlegcs\_integration\_test.go file.

* The package name must be servicemanager\_test.
* Include the //go:build integration build tag at the top of the file.
* The test should be self-contained, handling the setup and teardown of the emulator and the test bucket.
* Use require and assert from the testify suite for all validations.