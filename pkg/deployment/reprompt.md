## new deployment prompt

So far we have a servicemanager

Now we're expanding our infrastructure as a service to cover deployment of services and alerting

We want to have 3 separate strands to our infrastructure code

1) service management
2) service deployment
3) service monitoring and alerts

### First IAM though

We want IAM to be tightly controlled - 
deployed services should have the minimal roles necessary for their operations.

Now our first question we'd like input on is:

Service management creates pubsub, bigquery, storage buckets 

should it also setup IAM roles at the same time?

at the moment our MicroserviceArchitecture keeps track of dataflows and
their resources to add IAM we'd need to have an idea of the services these 
resources are connected to - for google 
(we'd like to plan for other deployment environments but google is the one we're most familiar with)
we would create a new service-account for each service - 
preferably automatic but for existing accounts we'd need to be able to fill them into the yaml

Another option is to include IAM in the deployment section of our CloudManager
The deployment code will by necessity know about the services deployed -
This could ask the servicemanager about the resources.

Can you give us advice on this and how we might structure it.

I have a basic GoogleIAM client here to show our basic thinking about IAM provisioning.


