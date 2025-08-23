# **Prompt 22: Integration Tests for Messaging Adapter**

**Objective:** Generate the complete integration test file for the googlemessaging.go adapter. This test will validate the adapter's interaction with the Google Cloud Pub/Sub API using an emulator.

**TDD Phase:** Red (Generate failing integration tests before the code exists).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the servicemanager/googlemessaging\_integration\_test.go file.

Write a complete integration test that validates the full lifecycle of Pub/Sub resources (Topic, Subscription) using the MessagingClient interface. The test **must not** use mocks; it should be written to connect to a Pub/Sub emulator.

**Key Requirements to Test:**

* **Resource Lifecycle:** The test must create a topic, then create a subscription on that topic, verify both exist, and then delete them (subscription first, then topic).
* **Configuration:** The test should validate that topic and subscription configurations (e.g., labels, ack deadline) are applied correctly.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in writing robust integration tests for Google Cloud services using official emulators.

**Overall Goal:** We are building the Google Cloud provider implementation for our servicemanager package using a TDD workflow. We need to write the integration tests that will specify and validate the behavior of our googlemessaging.go adapter.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/googlemessaging\_integration\_test.go**. This file will contain a test that uses a Pub/Sub emulator to validate the gcpMessagingClientAdapter.

**Key Requirements to Fulfill:**

* **Use Emulator:** The test setup must include logic to programmatically start and configure a Pub/Sub emulator. It should then create a real pubsub.Client configured to connect to this emulator.
* **Test Workflow:** The test **must** follow this sequence:
    1. **Create Topic:** Call the adapter's CreateTopicWithConfig method to create a new topic.
    2. **Create Subscription:** Call the adapter's CreateSubscription method to create a new subscription on the topic created in the previous step.
    3. **Verify Existence:** Use the real Pub/Sub client to verify that both the topic and the subscription actually exist in the emulator.
    4. **Teardown:** Call the adapter's Delete methods to first delete the subscription, and then the topic.
    5. **Verify Deletion:** Use the real Pub/Sub client to assert that both the topic and subscription no longer exist.

**Dependencies (Full source code to be provided):**

1. servicemanager/messagingadapter.go (contains the MessagingClient interface that the adapter will implement)
2. servicemanager/systemarchitecture.go (contains the TopicConfig and SubscriptionConfig structs)

Output Instructions:  
Generate the complete, well-commented Go code for the googlemessaging\_integration\_test.go file.

* The package name must be servicemanager\_test.
* Include the //go:build integration build tag at the top of the file.
* The test should be self-contained, handling the setup and teardown of the emulator and the test resources.
* Use require and assert from the testify suite for all validations.