### **execution_plan_01_foundation.md**

Phase: 1 of 4  
Objective: To establish the foundational Go code for the project by loading the core requirements into the LLM's context and generating the system's data schema and architectural interfaces. This phase is critical for grounding all subsequent code generation.

---

### **Step 1.1: Session Initialization and Context Priming**

**Goal:** To establish a clean session and load the high-level methodology, requirements, and TDD workflow into the LLM's context. This ensures the LLM understands its role, our collaborative process, and the project's constraints from the outset.

**Action:** Begin a new chat session. The very first message will be a composite of our core planning documents.

**Input Prompt to LLM:**

Plaintext

You are an expert Go developer and system architect. We are collaborating to build a declarative cloud orchestration tool.

I am providing you with the complete set of planning and requirements documents for this project. You must treat these documents as the definitive source of truth and maintain this context throughout our entire interaction. All code we generate must be verifiably traceable back to these requirements.

Our entire process will follow a strict Test-Driven Development (TDD) workflow: I will first provide prompts to generate test files (the "Red" phase), which will initially fail. I will then provide you with the interface and the failing test file, and you will write the implementation code to make the tests pass (the "Green" phase). Adherence to this TDD cycle is the most important rule of our engagement.

Here are the core documents for your context:

\<PASTE FULL CONTENT OF l1\_requirements.md\>  
\---  
\<PASTE FULL CONTENT OF l2\_requirement.md\>  
\---  
\<PASTE FULL CONTENT OF l3\_requirements.md\>  
\---  
\<PASTE FULL CONTENT OF prompt\_plan\_tdd.md\>

Please confirm that you have understood this methodology and are ready to be rooted in this requirements-driven, TDD-based process. Once you confirm, I will provide the first prompt to begin generating the foundational code.

Expected Output:  
A confirmation from the LLM, acknowledging its role, the TDD process, and its understanding of the provided requirements.  
Evaluation:  
Human evaluation. Does the LLM's response indicate a clear understanding of the core TDD directive? If not, reiterate the importance of the Red-\>Green cycle before proceeding.

---

### **Step 1.2: Generate Foundational Schema**

**Goal:** To generate the systemarchitecture.go file, which contains all the Go structs representing our YAML configuration.

**Action:** Deliver prompt\_01\_schema.md.

**Input Prompt to LLM:**

Plaintext

Excellent. Let's begin with the first foundational file.

\<PASTE FULL CONTENT OF prompt\_01\_schema.md\>

Expected Output:  
The complete, syntactically correct Go code for the servicemanager/systemarchitecture.go file.  
**Evaluation Criteria:**

1. **Static Analysis (Pass/Fail):** Save the output as systemarchitecture.go. Run go fmt and go vet on the file. Both must pass without errors.
2. **Compilation (Pass/Fail):** Run go build within the servicemanager package. It must compile successfully.
3. **Schema Compliance (Manual Spot-Check):**
    * Does every struct field have a yaml:"..." tag?
    * Do the tag names exactly match the keys specified in the prompt's schema definition?
    * Are all structs and fields correctly exported?

**Feedback Loop:**

* **On Success:** "Evaluation complete. systemarchitecture.go was generated successfully and passed all static analysis, compilation, and schema compliance checks. We will now proceed to the interface generation."
* **On Failure:** "The generated code failed evaluation. Here is the error from the go build command: \[PASTE COMPILER ERROR\]. Please analyze the error and provide a corrected version of the complete systemarchitecture.go file."

---

### **Step 1.3: Generate Foundational Interfaces**

**Goal:** To generate the complete set of Go interface files (...adapter.go, ...deployer.go, etc.) that define the architectural contracts and enable our provider-agnostic design.

**Action:** Deliver prompt\_02\_interfaces.md.

**Input Prompt to LLM:**

Plaintext

The schema is now in place. Next, we will generate all the foundational Go interfaces that define the contracts for our core modules.

\<PASTE FULL CONTENT OF prompt\_02\_interfaces.md\>

Expected Output:  
The complete Go code for all seven interface files specified in the prompt.  
**Evaluation Criteria:**

1. **Static Analysis (Pass/Fail):** Save all generated files to their respective packages (servicemanager, iam, etc.). Run go fmt ./... and go vet ./... from the project root. Both must pass.
2. **Compilation (Pass/Fail):** Run go build ./... from the project root. The entire project must compile successfully, verifying that all interface definitions and their references to the schema types are correct.
3. **Interface Purity (Manual Spot-Check):**
    * Do the interfaces avoid any types or concepts specific to a single cloud provider (e.g., no Google SDK types)?
    * Do the method signatures correspond to the lifecycle actions needed for the resources defined in the L3 requirements?

**Feedback Loop:**

* **On Success:** "Evaluation complete. All interface files were generated and passed all checks. The foundational schema and architectural contracts are now established. This concludes Phase 1\. We are now ready to begin the TDD cycles for the manager implementations."
* **On Failure:** "The generated code failed compilation. The cross-package dependencies between the new interfaces and the servicemanager schema appear to be incorrect. Here is the error from go build ./...: \[PASTE COMPILER ERROR\]. Please review the error and provide corrected versions of all affected files."