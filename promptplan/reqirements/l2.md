# **L2: Architectural & Non-Functional Requirements**

## **1\. Core Architecture Principles**

| ID | Requirement | Description |
| :---- | :---- | :---- |
| **1.1** | **Modularity & Separation of Concerns** | The system's core domains—resource lifecycle management, identity and access management (IAM), application deployment, and orchestration—**shall** be implemented as distinct, loosely-coupled modules with well-defined interfaces. |
| **1.2** | **Provider Abstraction** | The core logic of each domain **shall** be decoupled from any specific cloud provider's SDKs or APIs. This **must** be achieved through a provider/adapter design pattern, allowing concrete implementations to be plugged in. |
| **1.3** | **Idempotency** | All resource creation, update, and permission-granting operations **shall** be idempotent. Executing the same operation multiple times with the same configuration **must** result in the same final state without generating errors. |
| **1.4** | **Concurrency** | The system **shall** be designed to execute independent operations concurrently to maximize the efficiency of provisioning and deployment workflows. |
| **1.5** | **Declarative Configuration** | The system **shall** be driven by a declarative configuration that represents the desired final state of the architecture. The system's primary function is to reconcile the actual state with this desired state. |

## **2\. Reliability & Safety**

| ID | Requirement | Description |
| :---- | :---- | :---- |
| **2.1** | **Lifecycle Protection** | The system **shall** support a mechanism to protect resources from accidental deletion. Configuration **must** allow resources or groups of resources to be marked as permanent, which the teardown process **must** ignore. |
| **2.2** | **Atomic Policy Application** | When setting IAM policies on a resource, the system **shall**, by default, perform an atomic replacement of the entire policy to ensure it exactly matches the planned configuration and removes any out-of-band permissions. An exception for additive updates **must** be made for services with system-managed permissions (e.g., Cloud Run). |
| **2.3** | **Resiliency to Eventual Consistency** | The system **must** be resilient to the eventual consistency inherent in cloud APIs. Verification steps **shall** implement polling with configurable timeouts and retries to confirm that resources and IAM policies have successfully propagated. |

## **3\. Testability**

| ID | Requirement | Description |
| :---- | :---- | :---- |
| **3.1** | **Unit Testability** | All core logic modules **must** be unit-testable in isolation from external services. This is a direct consequence of the Provider Abstraction requirement (1.2). |
| **3.2** | **Integration Testability** | The system **shall** be designed to be testable against both local cloud emulators and real cloud environments to validate the full lifecycle of operations. |

## **4\. Future Capabilities (Forward-Looking Requirements)**

| ID | Capability | Description |
| :---- | :---- | :---- |
| **4.1** | **Robust State Management** | The system **shall** use a robust state management backend that supports remote storage and locking mechanisms. This is critical to prevent conflicts and ensure safe execution in multi-user or CI/CD environments. |
| **4.2** | **Extensibility Framework** | The system **should** define a clear plugin architecture or registration mechanism that allows third-party developers to add support for new resource types or cloud providers without modifying the core orchestration logic. |
| **4.3** | **Transactional Operations** | For complex, multi-step operations (e.g., deploying a new version of a service), the system **should** provide a mechanism for transactional rollbacks, automatically reverting to the last known good state upon failure. |

