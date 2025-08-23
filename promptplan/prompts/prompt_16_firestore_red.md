# **Prompt 16: Unit Tests for FirestoreManager**

**Objective:** Generate the complete unit test file for the FirestoreManager. These tests will serve as the executable specification for the FirestoreManager's implementation.

**TDD Phase:** Red (Generate failing tests before the code exists).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the servicemanager/firestoremanager\_test.go file.

Using the provided DocumentStoreClient interface, write a comprehensive suite of unit tests for a FirestoreManager struct that does not yet exist. The tests **must** use mocks for the DocumentStoreClient interface to validate all logic in isolation.

**Key Requirements to Test:**

* **Idempotency (L2-1.3):** The CreateResources test must cover both creating a new database and skipping an existing one.
* **Special Teardown Logic:** The Teardown test must verify that the manager does **not** attempt to delete a Firestore database, as this is a protected operation.
* **Verification:** The tests must cover successful and failed verification scenarios.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer with deep experience in writing thorough unit tests using the testify suite (mock, require, assert).

**Overall Goal:** We are continuing the TDD workflow for the servicemanager package. We have already defined the DocumentStoreClient interface. Our next step is to write the tests that will define and validate the behavior of the FirestoreManager.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/firestoremanager\_test.go**. This file will contain a full suite of unit tests for the FirestoreManager.

**Key Requirements to Fulfill:**

* **Use Mocks:** You **must** use testify/mock to create a MockDocumentStoreClient that implements the DocumentStoreClient interface.
* **Test Coverage:** You **must** create test cases for the following scenarios:
    * CreateResources:
        * Success case where a new database is created.
        * Success case where the database already exists and creation is skipped.
        * Failure case where the check for existence fails.
        * Failure case where the creation call fails.
    * Teardown:
        * A test to confirm that Teardown is a no-op. It should execute without error but **must not** call a Delete method on the client mock.
    * Verify:
        * Success case where the database exists.
        * Failure case where the database does not exist.

**Dependencies (Full source code to be provided):**

1. servicemanager/documentstore\_adapter.go (contains the DocumentStoreClient interface to be mocked)
2. servicemanager/systemarchitecture.go (contains the FirestoreDatabase struct)

Output Instructions:  
Generate the complete, well-commented Go code for the firestoremanager\_test.go file.

* The package name must be servicemanager\_test.
* Include all necessary imports.
* Create mock structs for MockDocumentStoreClient and MockDatabaseHandle.
* Write clean, readable tests with clear "Arrange, Act, Assert" sections.