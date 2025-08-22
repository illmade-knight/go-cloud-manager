# **Prompt 02: Core Service Interfaces**

**Objective:** Generate all the foundational Go interface files. These files define the contracts for each core module, decoupling the main orchestration logic from any specific cloud provider implementation.

## **Primary Prompt (Concise)**

Your task is to generate a series of Go interface files based on our architectural requirements. These interfaces are the abstraction layer that enables provider-agnostic logic.

**Files to Generate:**

1. servicemanager/storageadapter.go
2. servicemanager/messagingadapter.go
3. servicemanager/documentstore_adapter.go
4. servicemanager/scheduleradapter.go
5. iam/iamadapter.go
6. deployment/containerdeployer.go
7. prerequisites/prclient.go

For each file, create Go interfaces and any necessary related structs (e.g., BucketAttributes) that define the methods for interacting with that domain (e.g., CreateBucket, DeleteBucket). The methods should be generic and not specific to any cloud provider.

**Key Requirements to Fulfill:**

* **L2-1.2 (Provider Abstraction):** The interfaces are the direct implementation of this requirement.
* **L3-1.3 (Resource Specifications):** The methods in the interfaces should correspond to the lifecycle actions needed for the resources defined in the L3 requirements (e.g., if a GCSBucket is defined, the StorageClient interface needs methods to manage it).

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in building highly-testable, modular cloud infrastructure tools.

**Overall Goal:** We are continuing to build our declarative cloud orchestration tool. We have already generated the foundational data model in systemarchitecture.go. Now, we must create the abstraction layer that separates our core logic from the specifics of any cloud provider.

**Your Specific Task:** Your task is to generate the complete code for seven separate Go files, each containing the interface definitions for a core service domain. These interfaces are the contracts that the rest of our application will be built against.

**Key Requirements to Fulfill:**

* **L2-1.2 (Provider Abstraction):** This is the primary driver for this task. The interfaces you create are the embodiment of this requirement. They **must not** contain any types or concepts specific to a single cloud provider (e.g., no \*storage.BucketHandle from the Google SDK).
* **L3-1.3 (Resource Specifications):** The methods defined in your interfaces must be sufficient to manage the lifecycle of the resources detailed in our L3 requirements. For example, the iamadapter.go file must have an interface with methods like EnsureServiceAccountExists and ApplyIAMPolicy because the system needs to manage service accounts and their permissions.

**Dependencies:**

* The interfaces may need to reference the structs defined in servicemanager/systemarchitecture.go. You should assume that file is available.

Output Instructions:  
Generate the complete, well-commented Go code for each of the seven files listed below.

* Each file should be in its correct package (servicemanager, iam, deployment, prerequisites).
* Each file should contain a package-level comment explaining its purpose (e.g., "This file defines the generic interfaces for a storage client...").
* Interfaces and their methods should be clearly commented to explain their purpose and expected behavior.
* Create any necessary generic structs required by the interfaces (e.g., BucketAttributes for the storage adapter) to avoid direct dependencies on provider-specific types.

**Files to Generate:**

1. servicemanager/storageadapter.go
2. servicemanager/messagingadapter.go
3. servicemanager/documentstore_adapter.go
4. servicemanager/scheduleradapter.go
5. iam/iamadapter.go
6. deployment/containerdeployer.go
7. prerequisites/prclient.go