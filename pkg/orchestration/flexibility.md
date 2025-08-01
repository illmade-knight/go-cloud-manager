
# Today's task

## More flexible orchestration

We have an Infrastructure as Code set of packages to manage IAM, Resources, Deployment and Orchestration.

This works well at the moment but lacks some flexibility.

We have a basic commandline interface to our conductor at the moment that was *meant* to allow us
to choose which of the ordered phases to carry out. The idea would be initially to carry out all phases
in order but then on subsequent runs (or if we fail at any stage) we could choose only to update some phases.

### The phased order of operations

* -run-setup-iam=true
* -run-deploy-director=false
* -director-url=https://servicedirector-bq-flow-capgewwcva-nw.a.run.app
* -run-setup-resources=true
* -run-apply-iam=true
* -run-deploy-services=true

If we choose for instance not to deploy our service director then we can instead supply later phases with the URL
to an existing servicedirector.

However, the phases at the moment are too interdependent and wait for processes that never run to finish etc. 
We need to refactor to remove dependencies or explicitly ask for them where necessary 
(like in the servicedirector instance, where one or more of our deployments may need to access the servicedirector URL)

Our plan is to maximise flexibility while retaining the overall effectiveness of the code.

### Evaluate and Refactor

With the goal of flexibility we will run through the packages in our go-cloud-manager (github.com/illmade-knight/go-cloud-manager)
refactoring as we go. I will show the types within the package first and ask for a general refactoring plan. 
Then I will show the existing tests. 
At the beginning of each package refactor show a list of files that will be touched in the refactor. 
Update this list, marking off which files in our plan have been finished. 
If we refactor files later which affect previously refactored files the list ** MUST ** be updated to indicate the files need
additional work - This is a vital element in refactoring and you must keep track of these dependencies.

### Plan

Let's run through the library - I'll present an overview here, 
there's a very basic section for refactoring with a few specific elements we know need updating 
but this is not at all exhaustive, just some obvious things we noticed.

#### servicedirector
The servicedirector is a **deployed** service built on our /servicemanager package

The servicemanager package (github.com/illmade-knight/go-cloud-manager/pkg/servicemanager) 
handles resource creation and verification. 
It can be asked to create messaging (pubsub), cloud storage (gcs), bigquery resource etc. 
At present all concrete implementations are gcloud based 
but we follow an adapter pattern to allow us to use other solutions in future (Kafka, AWS buckets etc)

** required refactor **

Evaluate code, cleanup code, unify structure where possible (order of field, attributes in func etc)
Improve documentation and logging 

#### IAM
(github.com/illmade-knight/go-cloud-manager/pkg/iam)

Access management plans and set access policies on servicemanager created resources: pubsub, bigquery, firestore etc

** required refactor **

Evaluate code, cleanup code, unify structure where possible (order of field, attributes in func etc)
Improve documentation and logging

Ensure the way resources are recognized in MicroServiceArchitecture is uniform and application of roles is correct.

#### Deployment
(github.com/illmade-knight/go-cloud-manager/pkg/deployment) 

Buildpack based deployment of services using cloud build

** required refactor **

Evaluate code, cleanup code, unify structure where possible (order of field, attributes in func etc)
Improve documentation and logging

remove googleresourcemanager.go, this is used by a test but should be replaced by a helper that calls the IAM package

### Orchestration

(github.com/illmade-knight/go-cloud-manager/pkg/orchestration)

I am going to show you the orchestration package first but we will refactor it last.

I am showing it first so you get an overview of how we use the other packages. 

Take a brief look and confirm that these files give you some idea of the task ahead and then we'll begin the refactoring.



## Reiterate refactor requirements

* Show refactored files IN FULL
* Show refactor list and completed status at EACH STAGE
* Check for unused imports, variables, declarations before presenting code
* Update tests along with each file refactor.


