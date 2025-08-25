### **execution\_plan\_02\_managers\_tdd.md**

Phase: 2 of 4  
Objective: To generate the complete implementation for all core business logic managers (StorageManager, MessagingManager, etc.) using a strict, repeatable Red-\>Green Test-Driven Development (TDD) loop. Each manager will be unit-tested against a mocked adapter interface.

---

### **The TDD Loop: A Repeatable Pattern**

The following two steps—**Red** and **Green**—will be executed sequentially for each manager component. We will start with the StorageManager as the canonical example. The same process will then be repeated for all other managers.

---

### **Step 2.1 (Example: StorageManager \- Red Phase)**

**Goal:** To generate the storagemanager\_test.go file. This file acts as the executable specification for the StorageManager. It must use mocks and will fail to compile because the StorageManager struct does not yet exist.

**Action:** Deliver prompt\_10\_storage\_red.md along with its code dependencies.

**Input Prompt to LLM:**

Plaintext

Phase 1 was successful. We now have the foundational schema and interfaces.

We will now begin the TDD cycles for the manager implementations. For each manager, we will first generate the unit tests, then we will generate the implementation code to make those tests pass.

Let's start with the StorageManager. Your task is to generate the complete unit test file, \`storagemanager\_test.go\`.

\*\*Dependencies:\*\*  
Here is the interface your tests should be written against:  
\<PASTE FULL CONTENT OF servicemanager/storageadapter.go\>

Here are the schema structs your tests will use:  
\<PASTE FULL CONTENT OF servicemanager/systemarchitecture.go\>

\*\*Instructions:\*\*  
\<PASTE FULL CONTENT OF prompt\_10\_storage\_red.md\>

Expected Output:  
The complete Go code for the servicemanager/storagemanager\_test.go file.  
**Evaluation Criteria:**

1. **Static Analysis (Pass/Fail):** Run go fmt on the file.
2. **Compilation (CRITICAL \- Must Fail):** Run go test ./.... The expected and **correct** outcome is a **compile-time failure**. The error message should clearly state that the type StorageManager is undefined. This confirms the "Red" phase is successful. If the code *compiles*, the LLM has misunderstood and generated a stub implementation, which is a failure.

**Feedback Loop:**

* **On Success (Compile Failure):** "Red phase for StorageManager was successful. The test specification storagemanager\_test.go has been generated and fails to compile as expected. The executable specification is now in place. Proceeding to the Green phase."
* **On Failure (Compile Success or other errors):** "The Red phase failed. The generated test file did not produce the expected compile-time error. It should fail because StorageManager is not yet defined. Please regenerate the test file strictly according to the prompt, ensuring it only contains tests and mocks, not the implementation."

---

### **Step 2.2 (Example: StorageManager \- Green Phase)**

**Goal:** To generate the storagemanager.go file. The implementation must satisfy the StorageClient interface and make all tests in the previously generated storagemanager\_test.go file pass.

**Action:** Deliver prompt\_10b\_storage\_green.md along with its critical dependencies: the interface and the failing test file.

**Input Prompt to LLM:**

Plaintext

The Red phase was successful. We now have a failing test that defines the required behavior of our StorageManager.

Your task is now to write the implementation code that makes these tests pass. Generate the complete \`storagemanager.go\` file.

\*\*The Contract (Interface):\*\*  
\<PASTE FULL CONTENT OF servicemanager/storageadapter.go\>

\*\*The Specification (Failing Tests):\*\*  
\<PASTE FULL CONTENT OF servicemanager/storagemanager\_test.go\>

\*\*The Schema:\*\*  
\<PASTE FULL CONTENT OF servicemanager/systemarchitecture.go\>

\*\*Instructions:\*\*  
\<PASTE FULL CONTENT OF prompt\_10b\_storage\_green.md\>

Expected Output:  
The complete Go code for the servicemanager/storagemanager.go file.  
**Evaluation Criteria:**

1. **Execution (Pass/Fail):** Run go test ./... from the package root. The tests **must all pass**. This is the sole, binary criterion for success.

**Feedback Loop:**

* **On Success:** "Green phase for StorageManager was successful. The implementation has been generated, and all unit tests now pass. The component is complete. Proceeding to the next manager."
* **On Failure:** "Green phase failed. The code generated for storagemanager.go did not pass the tests. Here is the output from the go test command: \[PASTE FULL TEST FAILURE LOG\]. Please analyze the failure and provide a corrected version of the complete storagemanager.go file."

---

### **Step 2.3: Iteration for All Remaining Managers**

**Goal:** To complete the implementation of the servicemanager package by applying the TDD loop to all remaining managers.

**Action:** Repeat Step 2.1 (Red) and Step 2.2 (Green) for each of the following components, using their corresponding prompt files:

1. **MessagingManager**
    * Red: prompt\_12\_messaging\_red.md
    * Green: prompt\_12b\_messaging\_green.md
2. **BigQueryManager**
    * Red: prompt\_14\_bigquery\_red.md
    * Green: prompt\_14b\_bigquery\_green.md
3. **FirestoreManager**
    * Red: prompt\_16\_firestore\_red.md
    * Green: prompt\_16b\_firestore\_green.md
4. **SchedulerManager**
    * Red: prompt\_18\_scheduler\_red.md
    * Green: prompt\_18b\_scheduler\_green.md

Upon successful completion of all loops, this phase is concluded.