We have a servicemanager package - 
its purpose is to help organise setup and teardown of resources - 
it's doing a good job at that at the moment but 
now we're creating more complex dataflows it's showing some flaws. 
It sets up resources from an overall config, this creates all the resources for all services - 
this is fine but when a particular dataflow wants to check it is connecting to the correct resources and these resources exist it is not so good. 

Lets breakdown the architecture

We want our architecture to be a collection of microservices - 
each microservice will be deployed to a cloud environment.
At the moment we deploy all services to a single google cloud project, this reduces the complexity of things for the moment 
and lets take that as all we need for the moment.

Each microservice can require any of a number of external resources. 
The most common of these is messaging - topics and subscriptions. We also support 
* cloud storage
* bigquery
* firestore
* redis
we will also add more resources in future so we need to prepare for this

A dataflow is a conceptual subset of the overall architecture.
A dataflow should allow us to check all the resources for that dataflow exist.

(We'll tackle this later but remember: The teardown of a dataflows resources should only teardown resources that are not shared with other dataflows).

To start, for simplicity, we'd like to setup the overall structure all at once, in a similar way to at present.
But now we want to be able to verify a Dataflow - 
The request should be a Dataflow structure with all the required services, 
how they are connected by messaging topics and subscriptions, 
the overall resources required.

Then when setting up individual Services in the Dataflow we want to verify their required resources exit.

## Check
Lets just run through a test process and see if the proposed restructure will do what we hope.

Example usage:

we have an end to end test that tests load on a dataflow.

we define the dataflow.

at the start of the test we call for a full setup of all resources for the architecture 
(this will include the test defined dataflow of course). 
This should ensure all the resources for the dataflow under test are available.
(For our test structures we'll create and destroy all resources so we don't have to worry about anything being shared)

Now for the dataflow under test we'll ask servicemanager to verify the resources.

In the test we'll start to set up individual services. 

Let's say there are 2 services: serviceA and serviceB. 
* ServiceA has a topic.
* ServiceB has a subscription (linked to a topic), and uses Bigquery (a dataset and a table)

From the dataflow we should have references to ServiceA and ServiceB, 
their resources and the verification they exist
and we should be able to set up the service from the dataflow definition

## Dataflow

maybe this is the key, 
the dataflow at the moment lists all the global resources including those used by ServiceA and ServiceB.
but for service setup we want to see its resources by themselves.

## Start

OK lets start the restructure - 
lets go through everything one file at a time - 
can you show me each file refactor in full in the canvas and detail the changes in the chat. 
Let's start at the top level with servicemanager. 
After each files refactor I'll show you the existing unit test for that file and then we'll adapt it to the refactor. 
Don't change any code not directly touched by the refactor - check this carefully - 
the unit tests should help us verify this.



