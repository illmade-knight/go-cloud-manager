# **Prompt 28: Integration Tests for Scheduler Adapter**

**Objective:** Generate the complete integration test file for the googlescheduler.go adapter. This test will validate the adapter's interaction with the live Google Cloud Scheduler API.

**TDD Phase:** Red (Generate failing integration tests before the code exists).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the servicemanager/googlescheduler\_integration\_test.go file.

Write a complete integration test that validates the full lifecycle (Create, Verify, Delete) of a Cloud Scheduler job using the SchedulerClient interface. Since there is no official Cloud Scheduler emulator, the test **must** be written to run against a live GCP project.

**Key Requirements to Test:**

* **Resource Lifecycle:** The test must create a job targeting a Pub/Sub topic, verify it exists using the real Cloud Scheduler client, delete the job, and verify it has been removed.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in writing robust integration tests for Google Cloud services, and you are aware of which services have emulators and which do not.

**Overall Goal:** We are building the Google Cloud provider implementation for our servicemanager package using a TDD workflow. We need to write the integration tests that will specify and validate the behavior of our googlescheduler.go adapter.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/googlescheduler\_integration\_test.go**. This file will contain a test that uses the live Google Cloud Scheduler service to validate the gcpSchedulerClientAdapter.

**Key Requirements to Fulfill:**

* **No Emulator:** The test setup must acknowledge that there is no Cloud Scheduler emulator. It should be configured to run against a real, authenticated GCP project.
* **Prerequisite Resources:** The test **must** first create a temporary Pub/Sub topic to serve as the target for the Cloud Scheduler job. This topic should be cleaned up at the end of the test.
* **Test Workflow:** The test **must** follow this sequence:
    1. **Create Job:** Call the adapter's CreateJob method to create a new job targeting the temporary Pub/Sub topic.
    2. **Verify Creation:** Use a real scheduler.CloudSchedulerClient to get the job directly from the GCP API and assert that it exists.
    3. **Delete:** Call the adapter's Delete method on the job handle.
    4. **Verify Deletion:** Use the real Scheduler client to get the job again and assert that the call fails with a "not found" error.

**Dependencies (Full source code to be provided):**

1. servicemanager/scheduleradapter.go (contains the SchedulerClient interface that the adapter will implement)
2. servicemanager/systemarchitecture.go (contains the CloudSchedulerJob struct)

Output Instructions:  
Generate the complete, well-commented Go code for the googlescheduler\_integration\_test.go file.

* The package name must be servicemanager\_test.
* Include the //go:build integration build tag at the top of the file.
* The test should be self-contained, handling the setup and teardown of the prerequisite Pub/Sub topic and the scheduler job itself.
* Use require and assert from the testify suite for all validations.