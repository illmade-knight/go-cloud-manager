# **Prompt 04: Service Manager Google Cloud Adapters**

**Objective:** Generate the concrete Google Cloud Platform implementation adapters for the servicemanager interfaces. This code will bridge our generic, provider-agnostic interfaces to the specific Google Cloud SDKs.

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the Google Cloud adapter files within the servicemanager package. I will provide the specific adapter file to generate in each request.

You must write an adapter that correctly and completely implements the provided generic interface using the official, most modern, and idiomatic Google Cloud Go SDKs.

**Key Requirements to Fulfill:**

* **L2-1.2 (Provider Abstraction):** This file is the concrete implementation of the provider interface. It will be the only part of the servicemanager that is allowed to import and use Google Cloud-specific libraries like cloud.google.com/go/storage.
* **L3-2 (Supported Cloud Services):** This task directly fulfills the requirement to support the specified Google Cloud services.

**First File to Generate:** servicemanager/googlegcs.go

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in writing robust, production-grade clients for Google Cloud Platform services.

**Overall Goal:** We are building the Google Cloud provider implementation for our declarative cloud orchestration tool. We have already defined the generic interfaces for our resource managers. Now, we need to write the adapter code that implements these interfaces using the official Google Cloud Go SDK.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **servicemanager/googlegcs.go**. This file will contain the concrete implementation of the StorageClient interface for Google Cloud Storage (GCS).

**Key Requirements to Fulfill:**

* **L2-1.2 (Provider Abstraction):** Your code must implement all methods defined in the StorageClient, StorageBucketHandle, and BucketIterator interfaces. You will need to create adapter structs (e.g., gcsClientAdapter, gcsBucketHandleAdapter) that wrap the real Google Cloud SDK types (e.g., \*storage.Client, \*storage.BucketHandle).
* **Functionality:** The implementation must correctly translate the generic method calls to the corresponding cloud.google.com/go/storage SDK calls. This includes creating buckets, getting their attributes, updating them, and deleting them.
* **Type Conversion:** You must write private helper functions to convert between our generic BucketAttributes struct and the Google-specific storage.BucketAttrs struct.

### **Implementation Guidelines & Constraints**

This section provides important guardrails based on common pitfalls with the Google Cloud Go libraries.

* **Use Modern, Idiomatic GCP Libraries:** You **must** prefer the most recent, hand-written Google Cloud Go libraries. For example, for Pub/Sub, prefer the cloud.google.com/go/pubsub/v2 clients and pubsubpb types over older or auto-generated alternatives.
* **Distinguish Admin vs. Data Clients:** Be very careful to use the correct client for the task. For example, to manage a Firestore *database instance* (create, get), you **must** use the admin client: cloud.google.com/go/firestore/apiv1/admin. To manage *documents* within that database, the data-plane client (cloud.google.com/go/firestore) would be used.
* **Avoid Deprecated Libraries:** Do not use libraries from older, deprecated paths.
* **Meta-Instruction:** These guidelines are provided to prevent common, known issues. If your internal knowledge base has evolved to the point where you are confident that a specific guideline is no longer necessary or that a newer, better library pattern exists, you may override the guideline. If you do so, you **must** leave a code comment explaining your reasoning.

**Dependencies (Full source code to be provided):**

1. servicemanager/storageadapter.go (contains the StorageClient interface that must be implemented)
2. servicemanager/systemarchitecture.go (contains related structs if needed)

Output Instructions:  
Generate the complete, well-commented Go code for the googlegcs.go file.

* The package name must be servicemanager.
* The file should include a factory function (e.g., CreateGoogleGCSClient) that initializes the real \*storage.Client and returns it wrapped in our generic StorageClient interface.
* Handle errors correctly, especially "not found" errors, returning them so the manager logic can handle them appropriately.
* The code should be clean, efficient, and follow Go best practices.

*(After generating googlegcs.go, this prompt would be repeated for googlemessaging.go, googlefirestore.go, bigquery.go, etc., each time providing the relevant interface file and technical requirements.)*