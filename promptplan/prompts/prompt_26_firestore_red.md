# **Prompt 26: Integration Tests for Firestore Adapter**

**Objective:** Generate the complete integration test file for the googlefirestore.go adapter. This test will validate the adapter's interaction with the Google Cloud Firestore API using an emulator.

**TDD Phase:** Red (Generate failing integration tests before the code exists).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the servicemanager/firestoremanager\_integration\_test.go file.

Write a complete integration test that validates the lifecycle of a Firestore database using the DocumentStoreClient interface. The test **must not** use mocks; it should be written to connect to a Firestore emulator. Acknowledge that the Firestore emulator does not support the Admin API for checking existence, so a test-only adapter may be needed to work around this.

**Key Requirements to Test:**

* **Resource Lifecycle:** The test must attempt to create a database, verify its existence, and then run the teardown process (verifying that deletion is skipped).

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in writing robust integration tests for Google Cloud services, and you are aware of the common quirks and limitations of the emulators.

**Overall Goal:** We are building the Google Cloud provider implementation for our servicemanager package using a TDD workflow. We need to write the integration tests that will specify and validate the behavior of our googlefirestore.go adapter.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/firestoremanager\_integration\_test.go**. This file will contain a test that uses a Firestore emulator to validate the gcpFirestoreClientAdapter.

**Key Requirements to Fulfill:**

* **Use Emulator:** The test setup must include logic to programmatically start and configure a Firestore emulator.
* **Handle Emulator Limitations:** The Firestore emulator does not support the Admin API's GetDatabase call, which is used to check for existence. Your test **must** work around this. A good approach is to create a test-only implementation of the DocumentStoreClient and DatabaseHandle interfaces. This test adapter will:
    * Implement Exists() by attempting to create a data-plane client (firestore.NewClient). If this succeeds, the database "exists".
    * Implement Create() as a no-op, since the emulator starts with a database already created.
* **Test Workflow:** The test **must** follow this sequence:
    1. **Create:** Call the manager's CreateResources method. The underlying test adapter will handle the emulator's behavior.
    2. **Verify Existence:** Call the manager's Verify method. Assert that it passes.
    3. **Teardown:** Call the manager's Teardown method.
    4. **Verify Skipped Deletion:** Call the manager's Verify method again and assert that it still passes, confirming the database was not deleted.

**Dependencies (Full source code to be provided):**

1. servicemanager/documentstore\_adapter.go (contains the DocumentStoreClient interface)
2. servicemanager/systemarchitecture.go (contains the FirestoreDatabase struct)

Output Instructions:  
Generate the complete, well-commented Go code for the firestoremanager\_integration\_test.go file.

* The package name must be servicemanager\_test.
* Include the //go:build integration build tag at the top of the file.
* The test should be self-contained, handling the setup and teardown of the emulator.
* The implementation of the test-only adapter is a critical part of this task.
* Use require and assert from the testify suite for all validations.