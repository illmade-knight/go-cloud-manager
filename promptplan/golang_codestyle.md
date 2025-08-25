## **Go Language Coding & Refactoring Guidelines**

### **1\. General Principles**

* **Grounding:** Work **only** from the provided source files. Do not add, assume, or hallucinate any logic not explicitly present. Your analysis and output must be based solely on the provided code.  
* **Completeness:** When providing a refactored file, you **must** provide the **entire file content**, from the package declaration to the final line. Never use snippets, placeholders, stubs (// ...), or comments like // unchanged. The generated code must always be complete and syntactically correct.  
* **Precision:** A refactor must **never** change unrelated code. Apply only the requested change and its direct, necessary consequences.

---

### **2\. Code Style & Formatting**

* **Error Variable Declaration:** We prefer a more explicit style for error handling declarations.  
  * **Do This:**  
    Go  
    a, err := foobar(args...)  
    if err \!= nil {  
        // handle error  
    }

  * **Not This:**  
    Go  
    if a, err := foobar(args...); err \!= nil {  
        // handle error  
    }

* **Struct Literals:** Always use the expanded, multi-line format for struct literals, even for structs with few fields. This improves readability and simplifies diffs.  
  * **Do This:**  
    Go  
    user := \&User{  
        Name: "John Doe",  
        Age:  30,  
    }

  * **Not This:**  
    Go  
    user := \&User{Name: "John Doe", Age: 30}

* **Constructor Parameter Order:** Maintain a consistent order for parameters in constructor functions (New...). The preferred order is: context, configuration, clients/dependencies, then other parameters.  
  * Example: NewManager(ctx context.Context, cfg Config, dbClient DBClient, logger \*slog.Logger)

---

### **3\. Error Handling**

* **Handle All Critical Errors:** All functions that return an error must have the error value checked. Unhandled errors are a critical bug.  
* **Provide Context:** When returning an error from a downstream call, wrap it with context to create a meaningful error stack. Use the %w verb for this.  
  * Example: return fmt.Errorf("failed to create user %s: %w", userID, err)  
* **Log Non-Critical Errors:** Errors from operations like io.Closer.Close() on a reader can often be logged at an appropriate level (e.g., WARN or INFO) instead of being returned, if the failure has no impact on the function's outcome.

---

### **4\. Testing**

* **Test Package Naming:** Test files must belong to a \_test package (e.g., for a file in package cache, the test file's declaration is package cache\_test).  
* **Test Cleanup:** Prefer t.Cleanup() for scheduling cleanup tasks (e.g., closing connections, deleting resources). It's safer than defer as its execution is tied to the scope of the individual test function.  
* **Top-Level Test Context:** Every test suite or complex test should establish a top-level context.Context with a reasonable timeout. This prevents tests from hanging indefinitely.  
  * Example:  
    Go  
    func TestMyFeature(t \*testing.T) {  
        ctx, cancel := context.WithTimeout(context.Background(), 5\*time.Second)  
        t.Cleanup(cancel)  
        // ... rest of the test uses ctx  
    }

* **Avoid Sleeps:** Never use time.Sleep() to wait for an operation to complete. This creates flaky and slow tests. Instead, wait for a specific condition using a library function like require.Eventually or a custom polling loop with a timeout.  
* **Table-Driven Tests:** Use table-driven tests for testing multiple scenarios of the same function. This keeps tests clean, organized, and easy to extend.

---

### **5\. Documentation**

* **User-Facing Comments:** All public types, functions, and methods should have clear, user-focused godoc comments explaining their purpose.  
* **Refactoring Comments:** Comments related to a specific refactoring task should be clearly marked with a prefix like // REFACTOR: .... These comments should be removed once the changes are understood and accepted.