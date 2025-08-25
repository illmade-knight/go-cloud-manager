### **execution\_plan\_03\_adapters\_tdd.md**

Phase: 3 of 4  
Objective: To generate the complete implementation for all Google Cloud Platform (GCP) adapters using a strict Red-\>Green TDD loop. This phase is critical as it validates our generic interfaces against real-world cloud service APIs (or their emulators), ensuring our abstractions are correct and the final code is functional.

---

### **Key Differences from Phase 2**

This phase follows the same Red-\>Green pattern, but with two crucial distinctions:

1. **Integration Tests:** The tests generated are **integration tests**, not unit tests. They will interact with external services (emulators or live APIs) and will not use mocks.
2. **Build Tags:** All test files and test execution commands must use the integration build tag (//go:build integration) to separate them from the fast-running unit tests.

---

### **Step 3.1 (Example: GCS Adapter \- Red Phase)**

**Goal:** To generate the googlegcs\_integration\_test.go file. This file will define the required behavior of our GCS adapter by attempting to perform a full resource lifecycle against a GCS emulator. It will fail to compile because the concrete adapter struct does not yet exist.

**Action:** Deliver prompt\_20\_gcd\_adpater\_red.md along with its interface dependency.

**Input Prompt to LLM:**

Plaintext

Phase 2 was successful. All managers have been implemented and are fully covered by unit tests.

We will now proceed to Phase 3: implementing the concrete Google Cloud adapters. We will follow the same Red-\>Green TDD cycle, but this time we will generate integration tests that run against emulators or live APIs.

Let's start with the Google Cloud Storage (GCS) adapter. Your task is to generate the complete integration test file, \`googlegcs\_integration\_test.go\`.

\*\*Dependency (The Contract):\*\*  
Here is the interface the adapter must implement and that your test should be written against:  
\<PASTE FULL CONTENT OF servicemanager/storageadapter.go\>

\*\*Instructions:\*\*  
\<PASTE FULL CONTENT OF prompt\_20\_gcd\_adpater\_red.md\>

Expected Output:  
The complete Go code for the servicemanager/googlegcs\_integration\_test.go file, including the //go:build integration tag.  
**Evaluation Criteria:**

1. **Static Analysis (Pass/Fail):** Run go fmt on the file.
2. **Compilation (CRITICAL \- Must Fail):** Run go test \-tags=integration ./.... The test must **fail to compile** with an error indicating that the concrete adapter type (e.g., gcsClientAdapter) is undefined. This successful compile failure confirms the "Red" phase is complete.

**Feedback Loop:**

* **On Success (Compile Failure):** "Red phase for the GCS adapter was successful. The integration test googlegcs\_integration\_test.go fails to compile as expected. The specification for our concrete GCS implementation is now set. Proceeding to the Green phase."
* **On Failure (Compile Success or other errors):** "The Red phase failed. The generated integration test did not produce the expected compile-time error. It should fail because the GCS adapter implementation is missing. Please regenerate the test file strictly according to the prompt."

---

### **Step 3.2 (Example: GCS Adapter \- Green Phase)**

**Goal:** To generate the googlegcs.go file, which contains the concrete implementation of the StorageClient interface, using the Google Cloud SDK for GCS. This code must make the integration test pass.

**Action:** Deliver prompt\_20b\_gcd\_adpater\_green.md along with the interface and the failing integration test file.

**Input Prompt to LLM:**

Plaintext

The Red phase was successful. We now have a failing integration test that defines how our GCS adapter must interact with the GCS service.

Your task is to write the implementation of the GCS adapter in a new \`googlegcs.go\` file. This code must make the provided integration tests pass.

\*\*The Contract (Interface):\*\*  
\<PASTE FULL CONTENT OF servicemanager/storageadapter.go\>

\*\*The Specification (Failing Integration Tests):\*\*  
\<PASTE FULL CONTENT OF servicemanager/googlegcs\_integration\_test.go\>

\*\*Instructions:\*\*  
\<PASTE FULL CONTENT OF prompt\_20b\_gcd\_adpater\_green.md\>

Expected Output:  
The complete Go code for the servicemanager/googlegcs.go file.  
**Evaluation Criteria:**

1. **Execution (Pass/Fail):** With the GCS emulator running, execute go test \-tags=integration ./.... The tests **must all pass**. This provides high confidence that the adapter correctly interacts with the real GCS API.

**Feedback Loop:**

* **On Success:** "Green phase for the GCS adapter was successful. The implementation has been generated, and all integration tests now pass. The GCS adapter is complete. Proceeding to the next adapter."
* **On Failure:** "Green phase failed. The GCS adapter implementation did not pass the integration tests. Here is the output from the go test \-tags=integration command: \[PASTE FULL TEST FAILURE LOG\]. Please analyze the failure and provide a corrected version of the complete googlegcs.go file."

---

### **Step 3.3: Iteration for All Remaining Adapters**

**Goal:** To complete the Google Cloud provider implementation by applying the integration test TDD loop to all remaining adapters.

**Action:** Repeat Step 3.1 (Red) and Step 3.2 (Green) for each of the following components, using their corresponding prompt files:

1. **Messaging (Pub/Sub) Adapter**
    * Red: prompt\_22\_messaging\_red.md
    * Green: prompt\_22b\_messaging\_green.md
2. **BigQuery Adapter**
    * Red: prompt\_24\_bigquery\_red.md
    * Green: prompt\_24b\_bigquery\_green.md
3. **Firestore Adapter**
    * Red: prompt\_26\_firestore\_red.md
    * Green: prompt\_26b\_firestore\_green.md
4. **Scheduler Adapter**
    * Red: prompt\_28\_scheduler\_red.md
    * Green: prompt\_28b\_scheduler\_green.md

Upon successful completion of all loops, this phase is concluded, and the servicemanager package has a complete, tested, and functional implementation for Google Cloud Platform.