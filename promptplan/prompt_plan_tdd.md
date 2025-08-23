# **Prompt Planner: TDD Workflow**

## **1\. Guiding Philosophy**

Our strategy is to generate code using a **Test-Driven Development (TDD)** inspired workflow. For each module, we will first generate the tests that define its required behavior. These tests will then serve as the executable specification for generating the implementation code. This ensures that the generated code is correct by design and provides a clear, maintainable link between requirements and implementation.

## **2\. The TDD Prompting Workflow**

We will generate the repository by iterating through each package and applying the following three-step pattern for each logical component (e.g., a Manager or an Adapter).

### **Step 1: Generate the Interface (The Contract)**

* **Prompt:** A prompt to generate the clean, provider-agnostic Go interface for the component.
* **Example:** prompt\_02\_interfaces.md \-\> storageadapter.go

### **Step 2: Generate the Unit/Integration Tests (The Specification)**

* **Prompt:** A prompt that takes the **interface** from Step 1 as input. It instructs the LLM to generate a complete test file (\_test.go) using mocks (for unit tests) or emulators (for integration tests) that validates a struct which *will* implement that interface.
* **Result:** A test file that initially fails to compile because the implementation struct does not yet exist. This is the **"Red"** phase of TDD.

### **Step 3: Generate the Implementation (The Code)**

* **Prompt:** A prompt that takes both the **interface** from Step 1 and the **failing tests** from Step 2 as input.
* **Instruction:** "Your task is to write the Go code that implements the interface and makes all the provided tests pass."
* **Result:** An implementation file that is correct according to its specification. This is the **"Green"** phase of TDD.

This cycle will be repeated for each logical unit: Managers, Adapters, Orchestrators, etc. This ensures every piece of generated code is born from a test specification.

## **3\. Requirements Traceability**

To ensure our prompts and the resulting code stay aligned with our requirements, we will establish a traceability system.

| Step | Action | Description |
| :---- | :---- | :---- |
| **1\. Tagging** | **Maintain Unique IDs in Requirements** | We will continue to use the unique IDs (e.g., L1-1.1, L2-2.3) for every requirement in our .md files. |
| **2\. Mapping** | **Link Prompts to Requirements** | Each prompt will include a "Key Requirements to Fulfill" section that lists the specific requirement IDs it is designed to satisfy. |
| **3\. Validation** | **Automated Traceability Check** | A script can be run to parse all requirements and prompts, ensuring that every "shall" or "must" requirement is covered by at least one prompt. |

