# **Prompt 08: Master Orchestration Logic**

**Objective:** Generate the high-level orchestration logic that sequences and coordinates all other modules. This includes the main Conductor which manages the end-to-end workflow, and the sub-orchestrators for IAM and Deployments.

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the orchestration package. This includes the main Conductor and its helper orchestrators.

You must implement a Conductor that manages a stateful, multi-phase workflow, calling the IAM, Deployment, and ServiceManager modules in the correct dependency order. The workflow should support a remote, event-driven model for resource provisioning.

**Key Requirements to Fulfill:**

* **L1-1.2 (Automated Lifecycle Management):** The Conductor's Run() method is the primary implementation of the end-to-end automated workflow.
* **L2-1.1 (Modularity & Separation of Concerns):** The Conductor must act as a pure orchestrator, delegating all specific tasks to the appropriate sub-orchestrator or client (e.g., IAMOrchestrator, DeploymentManager, RemoteDirectorClient).

**Files to Generate:**

1. orchestration/conductor.go
2. orchestration/iamorchestrator.go
3. orchestration/deploymentorchestrator.go
4. orchestration/directorclient.go
5. orchestration/configgenerator.go

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer and system architect, specializing in designing complex, distributed workflows and CI/CD automation systems.

**Overall Goal:** We are building the top-level orchestration package. This is the "brain" of the entire system. It ties together the servicemanager, iam, and deployment modules into a coherent, sequential, and robust workflow.

**Your Specific Task:** Your task is to generate the complete code for the five core logic files in the orchestration package.

**Key Requirements to Fulfill:**

* **conductor.go:**
    * Implement the Conductor struct, which will hold instances of the other orchestrators and clients.
    * Implement the main Run() method. This method **must** execute the phases in the correct order:
        1. Check Prerequisites.
        2. Setup IAM (create SAs, apply project-level IAM, verify propagation).
        3. Prepare build artifacts (generate service-specific resources.yaml files).
        4. Build all service images in parallel and deploy the remote ServiceDirector.
        5. Trigger the remote ServiceDirector via the RemoteDirectorClient to provision foundational infrastructure.
        6. Verify resource-level IAM policies have propagated.
        7. Deploy the final application services in parallel.
    * The Conductor **must** be configurable via ConductorOptions to allow skipping phases.
* **iamorchestrator.go:**
    * Implement the IAMOrchestrator which uses the IAMClient and RolePlanner to handle all IAM-related tasks, including SA creation, polling for propagation, and applying/verifying policies.
* **deploymentorchestrator.go:**
    * Implement the DeploymentManager which uses the ContainerDeployer to orchestrate the parallel building of all service images and the subsequent parallel deployment of services within a dataflow.
* **directorclient.go:**
    * Implement the RemoteDirectorClient which communicates with the remote ServiceDirector over Pub/Sub. It must handle sending commands and asynchronously waiting for completion events.
* **configgenerator.go:**
    * Implement the logic to generate service-specific resources.yaml files by filtering the main architecture definition based on producer\_service and consumer\_service links.

**Dependencies (Full source code for interfaces to be provided):**

1. servicemanager/systemarchitecture.go
2. iam/iamadapter.go and iam/iamplanner.go
3. deployment/containerdeployer.go
4. prerequisites/prclient.go

Output Instructions:  
Generate the complete, well-commented Go code for all five files.

* Ensure the package name is orchestration.
* The code must be robust, thread-safe (especially with parallel operations), and provide clear logging for each phase and major step of the orchestration.
* The logic should correctly manage and pass state between phases (e.g., passing created SA emails to the IAM verification phase, and passing built image URIs to the deployment phase).