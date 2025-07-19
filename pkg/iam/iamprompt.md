We've done a bit more refactoring - so take a careful look at these files before we continue. 
We need to look in more detail at the IAM aspects of our application. 
At the moment we check for an IAM role for the servicemanager but we don't set up IAM resrouce access for the it. 

### Roles for servicemanager/servicedirector

We want the serviceAccount for the ServiceDirector to have all necessary roles.

While we're doing this we also want to think about how to make servicemanager (which servicedirector relies on)
a bit smarter.

These 2 things are linked 
- servicedirector should only have roles related to the dataflows it serves.
- servicemanager should only have managers for the resources it creates.

We would like to loop through the dataflows in our MicroserviceArchitecture

````
type MicroserviceArchitecture struct {
	Environment            `yaml:",inline"`
	ServiceManagerSpec     ServiceSpec              `yaml:"service_manager_spec"`
	Dataflows              map[string]ResourceGroup `yaml:"dataflows"`
	DeploymentEnvironments map[string]Environment   `yaml:"deployment_environments"`
}
````

and create the necessary managers and IAM roles 

Can you analyse these requirements and help us formulate a plan?