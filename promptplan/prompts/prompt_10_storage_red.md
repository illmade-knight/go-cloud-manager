# **Prompt 10: Unit Tests for StorageManager**

**Objective:** Generate the complete unit test file for the StorageManager. These tests will serve as the executable specification for the StorageManager's implementation.

**TDD Phase:** Red (Generate failing tests before the code exists).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the servicemanager/storagemanager\_test.go file.

Using the provided StorageClient interface, write a comprehensive suite of unit tests for a StorageManager struct that does not yet exist. The tests **must** use mocks for the StorageClient interface to validate all logic in isolation.

**Key Requirements to Test:**

* **L1-2.1 (Infrastructure Provisioning):** Test the full lifecycle: CreateResources, Verify, and Teardown.
* **L2-1.3 (Idempotency):** The CreateResources test must cover both creating a new bucket and updating an existing one.
* **L2-1.4 (Concurrency):** While difficult to test explicitly with mocks, the tests should be structured to handle multiple resources in the input spec.
* **L2-2.1 (Lifecycle Protection):** The Teardown test must verify that a bucket with teardown\_protection: true is not deleted.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer with deep experience in writing thorough unit tests using the testify suite (mock, require, assert).

**Overall Goal:** We are building the servicemanager package using a Test-Driven Development approach. We have already defined the StorageClient interface. Our next step is to write the tests that will define and validate the behavior of the StorageManager before we write the manager itself.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/storagemanager\_test.go**. This file will contain a full suite of unit tests for the StorageManager.

**Key Requirements to Fulfill:**

* **Use Mocks:** You **must** use testify/mock to create a MockStorageClient that implements the StorageClient interface. All tests must be written against this mock, not a real client.
* **Test Coverage:** You **must** create test cases for the following scenarios:
    * CreateResources:
        * Success case where all buckets are new and created successfully.
        * Success case where all buckets already exist and are updated successfully.
        * Partial failure case where one of several buckets fails to be created.
    * Teardown:
        * Success case where all buckets are deleted.
        * A case where a bucket with TeardownProtection enabled is correctly skipped and not deleted.
    * Verify:
        * Success case where all buckets are found.
        * Failure case where one of several buckets is not found.
* **Test Structure:** Use standard Go test table patterns where appropriate to cover multiple conditions efficiently.

**Dependencies (Full source code to be provided):**

1. servicemanager/storageadapter.go (contains the StorageClient interface to be mocked)
2. servicemanager/systemarchitecture.go (contains the GCSBucket struct)

Output Instructions:  
Generate the complete, well-commented Go code for the storagemanager\_test.go file.

* The package name must be servicemanager\_test.
* Include all necessary imports, including testing, github.com/stretchr/testify/assert, and github.com/stretchr/testify/mock.
* Create mock structs for MockStorageClient and MockStorageBucketHandle that fully implement their respective interfaces.
* Write clean, readable tests with clear "Arrange, Act, Assert" sections.