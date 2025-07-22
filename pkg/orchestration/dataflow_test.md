# **Resource Lifecycle Analysis for Dataflow E2E Test**

This document outlines every cloud resource created during the TestOrchestrator\_DataflowE2E integration test, along with its creation and teardown mechanism.

### **Summary**

The test correctly creates and tears down all temporary resources. The lifecycle is managed by a combination of the IAMOrchestrator, the main Orchestrator, the deployed ServiceDirector application, and the test's own t.Cleanup functions.

### **Table of Created Resources**

| Resource Type | Example Name | Created By | Teardown Method |
| :---- | :---- | :---- | :---- |
| **IAM** |  |  |  |
| Service Account | sd-sa-\<runID\> | IAMOrchestrator | iamOrch.Teardown() via t.Cleanup |
| Service Account | pub-sa-\<runID\> | IAMOrchestrator | iamOrch.Teardown() via t.Cleanup |
| Service Account | sub-sa-\<runID\> | IAMOrchestrator | iamOrch.Teardown() via t.Cleanup |
| Project IAM Binding | roles/pubsub.admin on sd-sa-... | IAMOrchestrator | Roles are cleaned by TestIAMClient.Close() |
| Resource IAM Binding | roles/pubsub.publisher on tracer-topic-... | IAMOrchestrator | Roles are cleaned by TestIAMClient.Close() |
| **Orchestrator C\&C** |  |  |  |
| Pub/Sub Topic | director-commands-\<runID\> | Orchestrator | orch.Teardown() via t.Cleanup |
| Pub/Sub Topic | director-events-\<runID\> | Orchestrator | orch.Teardown() via t.Cleanup |
| Pub/Sub Subscription | orchestrator-listener-\<uuid\> | Orchestrator | orch.Teardown() via t.Cleanup |
| **Dataflow Resources** |  |  |  |
| Pub/Sub Topic | tracer-topic-\<runID\> | ServiceDirector App | ServiceDirector App on SIGTERM |
| Pub/Sub Subscription | tracer-sub-\<runID\> | ServiceDirector App | ServiceDirector App on SIGTERM |
| **Test Verification** |  |  |  |
| Pub/Sub Topic | verify-topic-\<runID\> | Test setupVerificationListener | t.Cleanup in setupVerificationListener |
| Pub/Sub Subscription | verify-sub-\<runID\> | Test setupVerificationListener | t.Cleanup in TestOrchestrator\_DataflowE2E |
| **Services & Artifacts** |  |  |  |
| Cloud Run Service | sd-\<runID\> | Orchestrator | orch.TeardownServiceDirector() via t.Cleanup |
| Cloud Run Service | tracer-publisher-\<runID\> | Orchestrator | orch.TeardownServiceDirector() via t.Cleanup |
| Cloud Run Service | tracer-subscriber-\<runID\> | Orchestrator | orch.TeardownServiceDirector() via t.Cleanup |
| Container Image | .../tracer-publisher-\<runID\>:\<uuid\> | Cloud Build | Not torn down (by design) |
| GCS Source Object | \<proj\>\_cloudbuild/.../\<runID\>.tar.gz | CloudBuildDeployer | Deleted by CloudBuildDeployer |

### **Detailed Teardown Flow**

1. **Test Function Exits**: The TestOrchestrator\_DataflowE2E function completes (or fails).
2. **t.Cleanup Execution**: Go's testing framework executes the t.Cleanup functions in LIFO (Last-In, First-Out) order.
    * The main t.Cleanup is called.
    * It explicitly calls orch.TeardownServiceDirector() for each of the three deployed Cloud Run services, which triggers their deletion.
    * It calls orch.Teardown(), which deletes the orchestrator's command-and-control Pub/Sub topics and subscription.
    * It calls iamOrch.Teardown().
        * This calls iamClient.DeleteServiceAccount() for each SA. In "pooled" test mode, this is a fake delete that simply returns the account to the pool. In "standard" mode, it's a real delete.
        * Crucially, it then calls iamClient.Close(). For the TestIAMClient, this is the step that triggers the cleaning of all IAM roles from the service accounts in the pool.
    * The t.Cleanup inside setupVerificationListener is called, deleting the verify-topic.
    * The t.Cleanup that was created after the subscription is called, deleting the verify-sub.
3. **ServiceDirector SIGTERM**: When the sd-\<runID\> Cloud Run service is deleted, Cloud Run sends a SIGTERM signal to the container. The ServiceDirector application is designed to catch this signal and, as part of its graceful shutdown, delete the tracer-topic and tracer-sub resources it created.

This confirms that all temporary resources are accounted for and properly removed at the end of the test run.

gcloud projects add-iam-policy-binding gemini-power-test --member="user:tim@xythings.com" --role="roles/run.invoker"
        