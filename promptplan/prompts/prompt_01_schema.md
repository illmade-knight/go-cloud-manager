Prompt 01: System Architecture SchemaObjective: 
Generate the foundational Go data structures (systemarchitecture.go) that define the schema for the entire system's YAML configuration.Primary Prompt (Concise)Your task is to generate a single Go file named systemarchitecture.go. This file must contain Go structs that directly correspond to the YAML schema provided below.Every field in every struct must have a yaml:"..." tag that exactly matches the corresponding key in the YAML schema. The generated code should be complete, well-commented, and ready to be used with a YAML parsing library.YAML Schema to Implement:# L3-1: The system shall be configured using YAML files that conform to the schema defined below.

# L3-1.1: Root Document (`MicroserviceArchitecture`)
name: "production"
project_id: "gcp-project-123"
region: "us-central1"
location: "US"
service_manager_spec: { ... }
dataflows: { ... }

# L3-1.2: Service Specification (`ServiceSpec`)
name: "my-service"
service_account: "my-sa"
deployment: { ... }
dependencies: ["other-service"]
secret_environment_vars:
- name: "API_KEY"
  value_from: "api-key-secret"

# L3-1.3: Resource Specifications
# L3-1.3.1: Pub/Sub Topic (`TopicConfig`)
name: "my-topic"
producer_service: { ... }

# L3-1.3.2: Pub/Sub Subscription (`SubscriptionConfig`)
name: "my-subscription"
topic: "my-topic"
consumer_service: { ... }
ack_deadline_seconds: 60

# L3-1.3.3: GCS Bucket (`GCSBucket`)
name: "my-bucket"
location: "US-CENTRAL1"
storage_class: "STANDARD"
versioning_enabled: true
producers: [ ... ]
consumers: [ ... ]

# L3-1.3.4: BigQuery Dataset (`BigQueryDataset`)
name: "my_dataset"
location: "US"

# L3-1.3.5: BigQuery Table (`BigQueryTable`)
name: "my_table"
dataset: "my_dataset"
schema_type: "MyGoStructSchema"
time_partitioning_field: "event_timestamp"
clustering_fields: ["user_id"]
producers: [ ... ]
consumers: [ ... ]

# L3-1.3.6: Firestore Database (`FirestoreDatabase`)
name: "default-db"
location_id: "nam5"
type: "NATIVE"
producers: [ ... ]
consumers: [ ... ]

# L3-1.3.7: Cloud Scheduler Job (`CloudSchedulerJob`)
name: "my-cron-job"
schedule: "0 * * * *"
target_service: "my-service"
service_account: "cron-invoker-sa"
Reinforcement Prompt (Detailed Context)Persona: You are an expert Go developer specializing in building highly-testable, modular cloud infrastructure tools.Overall Goal: We are building a declarative cloud orchestration tool in Go. This tool reads a comprehensive YAML configuration defining a microservice architecture and then provisions, configures, and deploys it. The first step is to create the foundational data model that represents this YAML configuration.Your Specific Task: Your task is to generate the complete code for a single Go file named systemarchitecture.go. This file will contain all the necessary Go structs to accurately represent the system's configuration schema, which is defined in our technical requirements.Key Requirements to Fulfill:L3-1: The primary requirement is to create Go structs that perfectly match the detailed YAML schema provided in our technical specifications. Every field must have a corresponding yaml:"..." tag.L2-1.5 (Declarative Configuration): The generated structs are the in-memory representation of the declarative configuration. They must be robust and clear, as they will drive all subsequent logic in the system.Dependencies: This is the first file. It has no other code dependencies.Input Schema:(The same YAML schema from the primary prompt would be included here)Output Instructions:Generate the complete, well-commented Go code for the systemarchitecture.go file.The package name must be servicemanager.All structs and their fields must be exported (start with a capital letter).Include a package-level comment that explains the purpose of the file (i.e., defining the YAML schema).Add comments to each struct and field explaining its purpose, referencing the descriptions in the L3 requirements where appropriate.Ensure you include custom types where necessary, such as for Duration or LifecycleStrategy, to handle special YAML parsing logic.