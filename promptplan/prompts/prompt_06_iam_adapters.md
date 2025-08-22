# **Prompt 06: IAM Google Cloud Adapters**

**Objective:** Generate the concrete Google Cloud Platform implementation adapter for the iam package. This file will implement the IAMClient interface, providing a unified facade for managing IAM policies across multiple, distinct GCP services.

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the iam/googleiamclient.go file.

You must write an adapter that correctly implements the IAMClient interface. This implementation will act as a facade, using multiple underlying Google Cloud Go SDK clients (for Pub/Sub, Storage, Cloud Run, IAM Admin, etc.) to manage permissions on their respective resources.

**Key Requirements to Fulfill:**

* **L2-1.2 (Provider Abstraction):** This file is the concrete implementation of the IAMClient interface and will contain all the Google Cloud-specific IAM logic.
* **L2-2.2 (Atomic Policy Application):** The ApplyIAMPolicy method must handle the special case for Cloud Run services, performing an *additive* update to preserve system-managed permissions, while performing an atomic *replacement* for other resource types.
* **L3-2 (Supported Cloud Services):** The client must be able to set IAM policies on all required resource types, including Pub/Sub topics, GCS buckets, Cloud Run services, and Service Accounts.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in writing robust, production-grade clients for Google Cloud Platform services, with a deep understanding of the nuances of its IAM systems.

**Overall Goal:** We are building the Google Cloud provider implementation for our iam package. We have already defined the generic IAMClient interface and the high-level manager logic. Now, we need to write the adapter code that implements this interface using the various official Google Cloud Go SDKs.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **iam/googleiamclient.go**. This file will contain the GoogleIAMClient struct and all the methods required to satisfy the IAMClient interface.

**Key Requirements to Fulfill:**

* **Facade Pattern:** The GoogleIAMClient struct must hold instances of multiple underlying clients (e.g., \*pubsub.Client, \*storage.Client, \*run.Service, \*admin.IamClient).
* **Multi-Service IAM Logic:** The ApplyIAMPolicy, AddResourceIAMBinding, and CheckResourceIAMBinding methods must contain a switch statement based on the ResourceType of the binding. Each case will delegate the call to the appropriate underlying client for that specific service (e.g., a "pubsub\_topic" resource type uses the Pub/Sub client).
* **Specialized IAM Handling:**
    * For Cloud Run services, IAM updates **must be additive** to avoid overwriting default policies.
    * For BigQuery datasets, you must account for the API's legacy ACL system, translating modern IAM roles to their legacy equivalents for verification.
* **Service Account Lifecycle:** Implement the EnsureServiceAccountExists and DeleteServiceAccount methods using the cloud.google.com/go/iam/admin/apiv1 client.

### **Implementation Guidelines & Constraints**

* **Use Modern, Idiomatic GCP Libraries:** You **must** prefer the most recent, hand-written Google Cloud Go libraries. For example, for Pub/Sub, prefer the cloud.google.com/go/pubsub/apiv1 clients and pubsubpb types.
* **Distinguish Admin vs. Data Clients:** Be very careful to use the correct client for the task. For example, use cloud.google.com/go/iam/admin/apiv1 for managing Service Account resources.
* **Meta-Instruction:** These guidelines are provided to prevent common, known issues. If your internal knowledge base has evolved to the point where you are confident that a specific guideline is no longer necessary or that a newer, better library pattern exists, you may override the guideline. If you do so, you **must** leave a code comment explaining your reasoning.

**Dependencies (Full source code to be provided):**

1. iam/iamadapter.go (contains the IAMClient interface that must be implemented)
2. servicemanager/systemarchitecture.go (for context on resource types)

Output Instructions:  
Generate the complete, well-commented Go code for the googleiamclient.go file.

* The package name must be iam.
* The file should include a factory function (e.g., NewGoogleIAMClient) that initializes all the necessary underlying GCP clients and returns the unified GoogleIAMClient wrapped in the generic IAMClient interface.
* Implement retry logic with exponential backoff for operations that are prone to eventual consistency issues (e.g., setting a policy immediately after creating a resource).
* The code should be clean, robust, and handle all required resource types.