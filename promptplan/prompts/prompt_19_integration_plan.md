### **Plan: TDD for Google Cloud Adapters**

Our workflow for each adapter will be a specialized TDD cycle focused on integration testing:

1. **Red Phase (Integration Test):** We will write a prompt to generate a \_integration\_test.go file. This test will not use mocks. Instead, it will be written to run against a real (emulated) cloud service. It will test the full lifecycle (e.g., create, verify, delete) of a resource.
2. **Green Phase (Adapter Implementation):** We will then write a prompt to generate the adapter code itself (e.g., googlegcs.go). The primary requirement will be to make the integration test from the "Red" phase pass.

This ensures that the code we generate to interact with Google Cloud is verifiably correct.

---
