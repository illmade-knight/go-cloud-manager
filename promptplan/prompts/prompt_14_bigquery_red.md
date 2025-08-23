# **Prompt 14: Unit Tests for BigQueryManager**

**Objective:** Generate the complete unit test file for the BigQueryManager. These tests will serve as the executable specification for the BigQueryManager's implementation.

**TDD Phase:** Red (Generate failing tests before the code exists).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the servicemanager/bigquery\_test.go file.

Using the provided BQClient interface, write a comprehensive suite of unit tests for a BigQueryManager struct that does not yet exist. The tests **must** use mocks for the BQClient interface to validate all logic in isolation.

**Key Requirements to Test:**

* **Schema Validation:** The CreateResources test must verify that the manager validates that all SchemaTypes for tables exist in the schema registry before proceeding.
* **Idempotency (L2-1.3):** The CreateResources test must cover creating new datasets/tables and skipping existing ones.
* **Concurrency (L2-1.4):** The tests should be structured to handle multiple datasets and tables in the input spec.
* **Lifecycle Protection (L2-2.1):** The Teardown test must verify that protected datasets are not deleted.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer with deep experience in writing thorough unit tests using the testify suite (mock, require, assert).

**Overall Goal:** We are building the servicemanager package using a Test-Driven Development approach. We have already defined the BQClient interface. Our next step is to write the tests that will define and validate the behavior of the BigQueryManager.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/bigquery\_test.go**. This file will contain a full suite of unit tests for the BigQueryManager.

**Key Requirements to Fulfill:**

* **Use Mocks:** You **must** use testify/mock to create a MockBQClient that implements the BQClient interface. All tests must be written against this mock.
* **Test Coverage:** You **must** create test cases for the following scenarios:
    * Validate:
        * A success case where all table schemas are registered.
        * A failure case where a table's SchemaType is not found in the registry.
    * CreateResources:
        * Success case where new datasets and tables are created.
        * Success case where datasets and tables already exist and creation is skipped.
        * A failure case where creating a table fails.
    * Teardown:
        * Success case where datasets are deleted using DeleteWithContents.
        * A case where a dataset has TeardownProtection enabled and is correctly skipped.
    * Verify:
        * Success case where all datasets and tables are found.
        * Failure case where a table is missing.

**Dependencies (Full source code to be provided):**

1. servicemanager/bigquery.go (contains the BQClient interface to be mocked)
2. servicemanager/systemarchitecture.go (contains the BigQueryDataset and BigQueryTable structs)

Output Instructions:  
Generate the complete, well-commented Go code for the bigquery\_test.go file.

* The package name must be servicemanager\_test.
* Include all necessary imports.
* Create mock structs for MockBQClient, MockBQDataset, and MockBQTable.
* Remember to include a TestMain function to register a sample schema (e.g., TestSchema) so the validation tests can run.
* Write clean, readable tests with clear "Arrange, Act, Assert" sections.