# **Prompt 24: Integration Tests for BigQuery Adapter**

**Objective:** Generate the complete integration test file for the BigQuery adapter. This test will validate the adapter's interaction with the Google Cloud BigQuery API using an emulator.

**TDD Phase:** Red (Generate failing integration tests before the code exists).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the servicemanager/bigquery\_integration\_test.go file.

Write a complete integration test that validates the full lifecycle of BigQuery resources (Dataset, Table) using the BQClient interface. The test **must not** use mocks; it should be written to connect to a BigQuery emulator.

**Key Requirements to Test:**

* **Resource Lifecycle:** The test must create a dataset, then create a table within it, verify both exist, and then delete the dataset (which should also delete the table).
* **Configuration:** The test should validate that table configurations like partitioning and clustering are applied correctly.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in writing robust integration tests for Google Cloud services using official emulators.

**Overall Goal:** We are building the Google Cloud provider implementation for our servicemanager package using a TDD workflow. We need to write the integration tests that will specify and validate the behavior of our BigQuery adapter.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/bigquery\_integration\_test.go**. This file will contain a test that uses a BigQuery emulator to validate the bqClientAdapter.

**Key Requirements to Fulfill:**

* **Use Emulator:** The test setup must include logic to programmatically start and configure a BigQuery emulator. It should then create a real bigquery.Client configured to connect to this emulator.
* **Test Workflow:** The test **must** follow this sequence:
    1. **Create Dataset:** Call the adapter's Create method on a dataset handle.
    2. **Create Table:** Call the adapter's Create method on a table handle within that dataset. The table should be configured with time partitioning and clustering fields.
    3. **Verify Creation:** Use the real BigQuery client to fetch the metadata for both the dataset and the table directly from the emulator and assert that they exist and their configurations (e.g., partitioning type) are correct.
    4. **Teardown:** Call the adapter's DeleteWithContents method on the dataset handle.
    5. **Verify Deletion:** Use the real BigQuery client to assert that the dataset no longer exists.

**Dependencies (Full source code to be provided):**

1. servicemanager/bigquery.go (contains the BQClient interface that the adapter will implement)
2. servicemanager/systemarchitecture.go (contains the BigQueryDataset and BigQueryTable structs)

Output Instructions:  
Generate the complete, well-commented Go code for the bigquery\_integration\_test.go file.

* The package name must be servicemanager\_test.
* Include the //go:build integration build tag at the top of the file.
* The test should be self-contained, handling the setup and teardown of the emulator and the test resources.
* Remember to register a dummy schema for the test table.
* Use require and assert from the testify suite for all validations.