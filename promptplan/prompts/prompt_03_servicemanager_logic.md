# **Prompt 03: Service Manager Logic**

**Objective:** Generate the core logic for the servicemanager package. This includes the individual, concurrent, and idempotent resource managers for each cloud service type (Storage, Messaging, BigQuery, etc.).

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the resource manager files within the servicemanager package. I will provide the specific manager file to generate in each request.

You must implement the logic based on the provided systemarchitecture.go schema and the service adapter interfaces.

**Key Requirements to Fulfill:**

* **L1-2.1 (Infrastructure Provisioning):** The manager must handle the full lifecycle (create, update, verify, delete) of its specific resource type.
* **L2-1.3 (Idempotency):** The CreateResources method must be idempotent. If a resource already exists, it should be updated; otherwise, it should be created.
* **L2-1.4 (Concurrency):** The manager must process multiple resources of the same type in parallel.
* **L2-2.1 (Lifecycle Protection):** The Teardown method must check for and respect the teardown\_protection flag on a resource.

**First File to Generate:** servicemanager/storagemanager.go

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in building highly-testable, modular cloud infrastructure tools.

**Overall Goal:** We are building the core logic for the servicemanager package. This package is responsible for orchestrating the lifecycle of cloud resources. We have already defined the data schema and the provider-agnostic interfaces. Now, we need to implement the manager that uses these contracts to do the actual work.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/storagemanager.go**. This file will contain the StorageManager struct and its methods.

**Key Requirements to Fulfill:**

* **L1-2.1 (Infrastructure Provisioning):** Implement the CreateResources, Verify, and Teardown methods.
* **L2-1.3 (Idempotency):** The CreateResources method must first check if a bucket exists. If it does, it should update its attributes. If it does not, it should create it.
* **L2-1.4 (Concurrency):** Use Go routines and a wait group (sync.WaitGroup) to process all GCSBucket configurations from the input spec in parallel.
* **L2-2.1 (Lifecycle Protection):** The Teardown method must inspect the TeardownProtection field of each bucket configuration and skip deletion if it is set to true.
* **L2-2.3 (Resiliency):** The Teardown and Verify methods should treat a "not found" error as a success condition (e.g., a resource to be deleted is already gone), not a failure.

**Dependencies (Full source code to be provided):**

1. servicemanager/systemarchitecture.go (contains the GCSBucket struct)
2. servicemanager/storageadapter.go (contains the StorageClient and StorageBucketHandle interfaces that the StorageManager must use)

Output Instructions:  
Generate the complete, well-commented Go code for the storagemanager.go file.

* The package name must be servicemanager.
* The StorageManager struct should hold a StorageClient, a logger, and an environment config.
* Implement all methods to be thread-safe, especially when aggregating results or errors from multiple goroutines.
* Provide clear logging at the start and end of each public method and for key decisions (e.g., "Bucket already exists, updating.").

*(After generating storagemanager.go, this prompt would be repeated for messagingmanager.go, bigquerymanager.go, etc., each time providing the relevant interfaces and schema structs as dependencies.)*