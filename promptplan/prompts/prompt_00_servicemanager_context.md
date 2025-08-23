# **Prompt 00: Context for servicemanager Package Generation**

**Persona:** You are an expert Go developer and system architect. We are collaborating to build a declarative cloud orchestration tool.

Overall Goal:  
We are about to generate the complete Go code for the servicemanager package. This package is the core of our system, responsible for the lifecycle management (creation, verification, teardown) of cloud infrastructure resources like storage buckets, messaging topics, and database tables.  
Our Workflow (VERY IMPORTANT):  
We will be using a Test-Driven Development (TDD) approach. The process for generating this package will follow a strict, iterative sequence. Please conceptualize the entire plan as follows:

1. **Foundation First:** I will first ask you to generate two foundational files:
    * A systemarchitecture.go file containing all the data structures (the schema).
    * A series of ...adapter.go files containing the Go interfaces that define the contracts for our modules.
2. **TDD Cycles (Red \-\> Green):** After the foundation is laid, we will begin a series of cycles, one for each component (e.g., StorageManager, MessagingManager, GoogleGCSAdapter). Each cycle will consist of two distinct prompts:
    * **The "Red" Prompt (Tests):** I will first ask you to generate a \_test.go file. This test file will define the complete specification for a component that does not yet exist.
    * **The "Green" Prompt (Implementation):** I will then provide you with the interface and the failing test file, and your task will be to write the implementation code that makes all the tests pass.

How to Interpret the Following Prompts:  
Please maintain this context throughout our interaction for this package. Each subsequent prompt will be a specific step in this larger plan. Adhering to the TDD workflow is the most critical requirement. We will build the tests first, then the code that satisfies them.  
Let's begin with the first foundational file.