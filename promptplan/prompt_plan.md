# **Prompt Planner: A Strategy for Requirements-Driven Code Generation**

## **1\. Guiding Philosophy**

The goal is to create a structured, repeatable, and verifiable process for generating a complex Go application from our high-level requirements. The strategy hinges on a "divide and conquer" approach. Instead of one massive prompt, we will use a series of smaller, self-contained prompts, each responsible for generating a single file or a closely related set of interfaces.

This modular approach provides several advantages:

* **Clarity:** Each prompt has a narrow, well-defined scope.
* **Context:** We can provide only the most relevant requirements and code dependencies to the LLM for each task.
* **Testability:** It's easier to verify and debug the output of a small, focused prompt.
* **Maintainability:** If a requirement changes, we only need to update a small subset of prompts.

## **2\. The Prompting Workflow: A Phased Approach**

We will generate the repository in a logical order that mirrors a typical development process, ensuring that dependencies are created before they are needed.

### **Phase 1: The Foundation (Schema & Interfaces)**

* **Goal:** Generate the core data structures and interfaces that define the contracts for the entire system.
* **Input:** L3\_Technical\_Requirements.md (Section 1\) and L2\_Architectural\_Requirements.md (Section 1.2).
* **Prompts:**
    1. **Schema Prompt:** A single, high-priority prompt to generate the systemarchitecture.go file. This prompt will be given the full configuration schema from the L3 document. This file is the foundational data model for everything that follows.
    2. **Interface Prompts:** A series of prompts, one for each module, to generate the adapter interfaces (e.g., storageadapter.go, messagingadapter.go, iamadapter.go, containerdeployer.go). These prompts will be given the L2 requirement for "Provider Abstraction" and the relevant L3 resource specifications.

### **Phase 2: The Implementation (Managers & Adapters)**

* **Goal:** Implement the core logic for each module.
* **Input:** The generated schema and interface files, plus relevant L1, L2, and L3 requirements.
* **Prompts:** We will iterate through each package (servicemanager, iam, deployment, etc.) and generate the implementation files. Each prompt will follow a consistent structure:
    * **Persona:** "You are an expert Go developer specializing in cloud infrastructure tools."
    * **Overall Goal:** State the purpose of the package (e.g., "We are building the servicemanager package...").
    * **Specific Task:** Clearly state the file to be generated (e.g., "Your task is to write the storagemanager.go file.").
    * **Key Requirements:** Provide specific, numbered requirements from the L1, L2, and L3 documents that the file must satisfy (e.g., L2-1.3 Idempotency, L2-1.4 Concurrency, L3-1.3.3 GCS Bucket schema).
    * **Dependencies:** Provide the full source code for the Go interfaces the file must implement and the schema structs it will use.
    * **Output:** "Generate the complete, well-commented Go code for this file."

### **Phase 3: The Glue (Orchestration)**

* **Goal:** Generate the top-level orchestration logic that ties all the modules together.
* **Input:** The generated manager interfaces from Phase 2\.
* **Prompts:** A series of prompts to generate the files in the orchestration package. The prompt for the conductor.go file, for example, will be given the interfaces for the IAMOrchestrator and DeploymentManager so it knows what tools it has available to coordinate.

## **3\. Requirements Traceability: The Feedback Loop**

To ensure our prompts and the resulting code stay aligned with our requirements, we will establish a traceability system.

| Step | Action | Description |
| :---- | :---- | :---- |
| **1\. Tagging** | **Maintain Unique IDs in Requirements** | We will continue to use the unique IDs (e.g., L1-1.1, L2-2.3) for every requirement in our .md files. |
| **2\. Mapping** | **Link Prompts to Requirements** | Each prompt in our prompt suite (which we can also store in version control) will include a metadata block that lists the specific requirement IDs it is designed to fulfill. |
| **3\. Validation** | **Automated Traceability Check** | We will create a simple script that performs two checks: 1\. It parses all requirements documents and verifies that every requirement with a "shall" or "must" has at least one prompt mapped to it. 2\. It checks that every prompt maps to a valid, existing requirement ID. |
| **4\. Maintenance** | **Run Check in CI** | This traceability check will be run as part of our CI pipeline. A change to a requirements document that is not reflected in the prompt suite (or vice-versa) will cause the check to fail. This creates a feedback loop that forces the prompts and requirements to remain in sync. |

This structured approach ensures that we are not just generating code, but building a system that is demonstrably and verifiably aligned with its documented requirements.