# **L3: Technical & Configuration Requirements**

## **1\. Configuration Schema**

The system **shall** be configured using YAML files that conform to the schema defined below.

### **1.1. Root Document (MicroserviceArchitecture)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | A unique name for the environment (e.g., "production"). |
| project\_id | String | Yes | The target Google Cloud project ID. |
| region | String | No | The default GCP region for regional resources (e.g., "us-central1"). |
| location | String | No | The default GCP location for multi-regional resources (e.g., "US"). |
| service\_manager\_spec | Object | Yes | The configuration for the remote orchestrator service. |
| dataflows | Map | Yes | A map of ResourceGroup objects, keyed by dataflow name. |

### **1.2. Service Specification (ServiceSpec)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | The unique name of the microservice. |
| service\_account | String | Yes | The logical name of the IAM Service Account the service will run as. |
| deployment | Object | Yes | The DeploymentSpec object defining how the service is built and deployed. |
| dependencies | List | No | A list of other service names this service needs to invoke. |
| secret\_environment\_vars | List | No | A list of secrets to mount as environment variables. |

### **1.3. Resource Specifications**

The system **shall** support the configuration of the following resources within a ResourceGroup.

#### **1.3.1. Pub/Sub Topic (TopicConfig)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | The name of the Pub/Sub topic. |
| producer\_service | Object | No | A ServiceMapping object linking the service that publishes to this topic. |

#### **1.3.2. Pub/Sub Subscription (SubscriptionConfig)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | The name of the Pub/Sub subscription. |
| topic | String | Yes | The name of the topic this subscription is attached to. |
| consumer\_service | Object | No | A ServiceMapping object linking the service that consumes from this subscription. |
| ack\_deadline\_seconds | Integer | No | The acknowledgement deadline in seconds. |

#### **1.3.3. GCS Bucket (GCSBucket)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | The globally unique name of the GCS bucket. |
| location | String | Yes | The geographic location of the bucket (e.g., "US-CENTRAL1"). |
| storage\_class | String | No | The default storage class (e.g., "STANDARD"). |
| versioning\_enabled | Boolean | No | Enables object versioning on the bucket. |
| producers / consumers | List | No | Lists of ServiceMapping objects to grant write/read access. |

#### **1.3.4. BigQuery Dataset (BigQueryDataset)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | The name of the BigQuery dataset. |
| location | String | Yes | The geographic location of the dataset (e.g., "US"). |

#### **1.3.5. BigQuery Table (BigQueryTable)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | The name of the BigQuery table. |
| dataset | String | Yes | The parent dataset for the table. |
| schema\_type | String | Yes | A key that maps to a registered Go struct for schema inference. |
| time\_partitioning\_field | String | No | The column name to use for time-based partitioning. |
| clustering\_fields | List | No | A list of column names to cluster data on. |
| producers / consumers | List | No | Lists of ServiceMapping objects to grant write/read access. |

#### **1.3.6. Firestore Database (FirestoreDatabase)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | The logical name for the Firestore database instance. |
| location\_id | String | Yes | The multi-region location (e.g., "nam5"). |
| type | String | Yes | The database mode (NATIVE or DATASTORE\_MODE). |
| producers / consumers | List | No | Lists of ServiceMapping objects to grant write/read access. |

#### **1.3.7. Cloud Scheduler Job (CloudSchedulerJob)**

| Field | Type | Required | Description |
| :---- | :---- | :---- | :---- |
| name | String | Yes | The name of the scheduler job. |
| schedule | String | Yes | The job schedule in cron format. |
| target\_service | String | Yes | The name of the service to be invoked by the job. |
| service\_account | String | Yes | The service account the job will use to invoke the target. |

## **2\. Supported Cloud Services (Initial Scope: GCP)**

The system **shall** provide concrete implementations (adapters) for the following Google Cloud services:

| Category | Service |
| :---- | :---- |
| **Compute** | Google Cloud Run |
| **Storage** | Google Cloud Storage |
| **Messaging** | Google Cloud Pub/Sub |
| **Database** | Google Cloud Firestore, Google BigQuery |
| **Scheduling** | Google Cloud Scheduler |
| **Build** | Google Cloud Build, Google Artifact Registry |
| **Security** | Google IAM, Google Secret Manager |
| **Prerequisites** | Google Service Usage API |

## **3\. Future Capabilities (Forward-Looking Requirements)**

| ID | Capability | Description |
| :---- | :---- | :---- |
| **3.1** | **New Resource Support** | The system **should** be extended to support additional GCP services, including but not limited to: Cloud SQL, Memorystore (Redis), and Cloud Tasks. |
| **3.2** | **Alternative Deployment Targets** | The system **should** be extended to support deploying services to Google Kubernetes Engine (GKE) as an alternative to Cloud Run. |
| **3.3** | **Alternative Configuration Language** | The system **could** be enhanced to support HashiCorp Configuration Language (HCL) as an alternative to YAML, enabling more complex logic, variables, and expressions in the configuration files. |

