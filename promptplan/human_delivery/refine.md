Phases 1, 2, and 3 are now complete. We have a fully functional and robustly tested `servicemanager` package.

The final phase is refinement. Your role now shifts to that of a senior Go architect. I am providing you with the complete source code for the entire package. Your task is to perform a holistic code review based on the following comprehensive guidelines and provide a report of your findings.

---
### ## Master Refinement Prompt

#### **1. Core Directives**
* **Grounding:** Work **only** from the provided source files. Do not add, assume, or hallucinate any logic not explicitly present.
* **Precision:** A refactor must **never** change unrelated code. Propose changes first and wait for agreement before implementing.

#### **2. Go Language & Style Guide**
* **Error Declaration:** Avoid the `if short-form; err != nil` pattern.
* **Struct Literals:** Always use the expanded, multi-line format.
* **Constructor Order:** Use the `context`, `config`, `dependencies` parameter order.
* **Error Wrapping:** Wrap downstream errors with context using `fmt.Errorf("... %w", err)`.
* **Testing Standards:** Use `package foobar_test`, prefer `t.Cleanup()`, use top-level contexts with timeouts, and avoid `time.Sleep()` in favor of `require.Eventually`.

#### **3. Refactoring & Presentation Rules**
* **Completeness:** Always provide the **entire file content**.
* **No Placeholders:** Never use stubs, skeletons, or comments like `// ...`.
* **Focus on Changes:** Do not show unchanged files.

#### **4. Documentation Standards**
* **User-Facing Comments:** Ensure all public members have clear `godoc` comments.
* **Refactoring Comments:** Prefix temporary comments with `// REFACTOR:`.

#### **5. Your Immediate Task**
Review the complete codebase I am about to provide against all the rules above.
1.  **First, provide a report.** This report should be a markdown list of your findings. Focus on identifying duplicated code, opportunities for abstraction, and any deviations from the guidelines.
2.  **Then, wait for my confirmation.** Do not implement any changes until I have reviewed your report.
---

**Source Code for Review:**
<PASTE FULL CONTENT of all generated .go files in the servicemanager package>