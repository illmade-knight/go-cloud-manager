# **Prompt 07: Deployment Logic & Adapters**

**Objective:** Generate the core logic and the Google Cloud implementation for the deployment package. This includes the CloudBuildDeployer that orchestrates the build-and-deploy pipeline and its underlying adapters for GCS, Artifact Registry, Cloud Build, and Cloud Run.

## **Primary Prompt (Concise)**

Your task is to generate the Go code for the deployment/cloudbuilddeployer.go file.

You must implement the ContainerDeployer interface. The implementation should orchestrate a multi-step process:

1. Ensure a GCS bucket and Artifact Registry repository exist.
2. Package and upload source code to the GCS bucket.
3. Trigger a Google Cloud Build job using Cloud Native Buildpacks (pack build) to create a container image.
4. Deploy the newly built image to Google Cloud Run.

**Key Requirements to Fulfill:**

* **L1-2.3 (Application Build & Deploy):** This file is the primary implementation of the end-to-end build and deployment workflow.
* **L2-1.1 (Modularity):** The CloudBuildDeployer should act as an orchestrator, delegating specific API interactions to smaller, single-purpose adapters for each service (Storage, Artifact Registry, Cloud Build, Cloud Run).
* **L3-2 (Supported Cloud Services):** The implementation must use the correct Google Cloud SDKs for all interactions.

## **Reinforcement Prompt (Detailed Context)**

**Persona:** You are an expert Go developer specializing in CI/CD and cloud-native build and deployment automation on Google Cloud Platform.

**Overall Goal:** We are building the deployment package, which is responsible for turning a service's source code into a running application. We have already defined the necessary interfaces. Now, we need to write the main orchestrator and its concrete Google Cloud implementations.

**Your Specific Task:** Your task is to generate the complete code for a single Go file: **deployment/cloudbuilddeployer.go**. This file will contain the CloudBuildDeployer struct and its methods, as well as the concrete adapter implementations for the various Google Cloud services it depends on.

**Key Requirements to Fulfill:**

* **Orchestration Logic:** The BuildAndDeploy method must execute the full pipeline in the correct sequence. You should also implement separate Build and DeployService methods to allow for a more flexible, two-phase orchestration where all services can be built in parallel before any are deployed.
* **Cloud Build Configuration:** The triggered Cloud Build job **must** be configured to use the gcr.io/k8s-skaffold/pack builder to execute a Cloud Native Buildpack build. The configuration must correctly handle monorepo builds by allowing for a BuildableModulePath and copying go.mod/go.sum files as a preliminary step if necessary.
* **Adapter Implementation:** Within the same file, implement the unexported googleStorageAdapter, googleArtifactRegistryAdapter, googleCloudBuildAdapter, and googleCloudRunAdapter structs. These adapters will contain the direct calls to the respective Google Cloud SDKs.
* **Idempotency (L2-1.3):** The logic for ensuring the GCS bucket and Artifact Registry repository exist must be idempotent. If they already exist, the process should continue without error. The Cloud Run deployment logic should patch an existing service or create a new one if it doesn't exist.

### **Implementation Guidelines & Constraints**

* **Use Modern, Idiomatic GCP Libraries:** You **must** use the most recent, idiomatic Google Cloud Go libraries for all services (storage, artifactregistry, cloudbuild, run).
* **Handle Long-Running Operations:** Calls that trigger long-running operations (like creating a repository, a build, or a service deployment) **must** include logic to poll and wait for the operation to complete successfully before proceeding.

**Dependencies (Full source code to be provided):**

1. deployment/containerdeployer.go (contains the ContainerDeployer interface to be implemented)
2. servicemanager/systemarchitecture.go (contains the DeploymentSpec struct)

Output Instructions:  
Generate the complete, well-commented Go code for the cloudbuilddeployer.go file.

* The package name must be deployment.
* Include a factory function NewCloudBuildDeployer that initializes the main struct and all its underlying Google Cloud clients and adapters.
* The code must be robust, handling potential errors from the API calls and providing clear log messages for each step of the process.