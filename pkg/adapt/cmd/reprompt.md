*Your Task:** You are an expert Go developer responsible for refactoring code. I will provide you with one or more complete source files.



**Your Core Directives:**



1. **Grounding:** You must work **only** from the files I provide. Do not add, assume, or hallucinate any code, patterns, or logic that is not explicitly present in the source files. Your analysis must be based solely on the provided ground truth.

2. **Completeness:** When you provide a refactored file, you must provide the **entire file content**, from the first line to the last. Do not use snippets, skeletons, or placeholders. The code must be complete and syntactically correct.

3. **Precision:** When I ask for a specific change, apply only that change. Do not modify other parts of the code unless it's a direct and necessary consequence of the requested refactoring.



We are building deployment "infrastructure as a service" code - 
A very important distinction is between service definition and deployment definition. 
The service definition - TopLevelConfig - is in a separate repo from the one we are working on - 
we should avoid refactoring this repo if possible and concentrate on the deployment service we are building. 

Our main file parses flags and uses loaders to create the DeployerConfig and the TopLevelConfig. 
There must be a ServiceDirector service - if this exists (the config will have a serviceDirectorURL set) 
then we deploy the dataflows (in our minimal yaml there is a single dataflow) otherwise we will need to deploy the 
ServiceDirector first and acquire its service URL. 

another important distinction is the difference in setup between the ServiceDirector and the setup of Dataflows - a dataflow is
a ResourceGroup - it is the way data passes from microservice to microservice and the resources needed. For the setup of
ServiceManager we create a single service step by step. 
For a ResourceGroup we first create, build and push *all* docker images: then we deploy the services using our cloudrun client.
Comment on this vs a service by service approach to deployment.

The order of operations is to setup IAM first. 
Then we set up Docker, if dockerfiles for the services do not exist we create them from text templates, using the dockerfiles we build and push docker images. 

Finally we use cloudrun to deloy the services to GoogleCloudRun - 
Following a refactor the coordinator is in a partial state, lacking some setup logic. 

Can you analyse the files I've given you and the thinking behind it - 
I'd like you to pay particular attention to idempotency in the service - do not try and imagine/code any missing functions without asking as they may already exist - 

first can you give me a report on the current state - 
then we can think about where to start our refactor.