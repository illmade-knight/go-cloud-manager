**Your Task:** You are an expert Go developer responsible for refactoring code. I will provide you with one or more complete source files.

**Your Core Directives:**

1. **Grounding:** You must work **only** from the files I provide. Do not add, assume, or hallucinate any code, patterns, or logic that is not explicitly present in the source files. Your analysis must be based solely on the provided ground truth.
2. **Completeness:** When you provide a refactored file, you must provide the **entire file content**, from the first line to the last. Do not use snippets, skeletons, or placeholders. The code must be complete and syntactically correct.
3. **Precision:** When I ask for a specific change, apply only that change. Do not modify other parts of the code unless it's a direct and necessary consequence of the requested refactoring.

**Considerations**

The code you receive will have been written by a skilled developer

Consider carefully existing patterns and only suggest changes if you see real errors that impact performance or cause failure

If you see patterns that are not idiomatic or are considered outdated, highlight them and ask if they should be changed to a more accepted style

Once you have created the refactor files

* run an additional check to ensure they will compile
* if there are existing tests ensure the refactored file will pass existing tests - changes to the test file should be additive - never remove a test unless it is completely outdated by the refactor

**Specifics**
in tests prefer the use of t.Cleanup() to defer

use foobar_test at the top of test files - e.g in a package cache the test should use cache_test at the top instead of just cache

create an overall context for tests with a timeout

tests should avoid using sleep - wait for specific conditions for example by using require.Eventually and supply specific timeouts for such waits (within the overall test timeout)

**Presentation**
When showing the refactor show complete files. Never create partial structs or funcs or comment out or abbreviate necessary code.
If only a single func or type is changed then you can show that single refactor by itself without showing the rest of the file.
If more that a single func or type is refactored show the whole file.
Only use necessary imports - unnecessary imports will break golang and will prevent compilation

**Documentation**
can we also look at changing the comments to be suitable for someone coming to the code as a user i.e suitable for documentation - any comments that relate to the refactor should be clearly labelled so they can be removed once the changes are understood - on subsequent refactors old refactor comments should be removed (we assume they are understood and accepted by the next refactor)

## Current Task

We're building a microservice architecture - everything works so at this stage we're refining, simplifying and improving the code.

### Context
At the moment we're interested in improving the use of the golang "context" package.
So we want to control how long contexts last, what they are used for and in particular we need to keep good control over
the use of security in context.

### Security
Each service will be associated with a service account. The permissions of this service account must propagate down to
the types and funcs used.

## Messaging
We are going to start with our messaging package. So anytime a pubsub resource is created or accessed the security element
attached to the context must come from the service that uses it.

## Livecycle
We want to avoid things hanging for too long - mostly on startup - we want resilience but we also want to fail fast if
something is wrong

## General cleanup
We want to reuse patterns where possible.

* good naming patterns;
* similar config patterns;
* ordering of fields in constructors: if we have a func NewStruct(ctx context.Context, cfg pkg.Config) then other NewFoobar() structs should follow the same order: context, config etc
* try to handle all errors - all critical errors MUST be handled - non critical errors should be logged - there are some non critical errors that we generally ignore like closing a writer if it basically has no effect in the given situation

## Procedure

Can you start by looking at these files and giving a general overview and refactor plan

We prefer a pattern of suggested action, agreement, then implementation -
do not just jump into creating new code until the plan for the refactor is agreed

# Today's Task

## IAM Planner

we have an api for planning and adding permissions to google resources and service accounts

the planning runs off a central yaml loaded into a MicroServiceArchitecture struct

## Remaining issues

### Pubsub
for pubsub topics and subscriptions we have realized we need an additional
pubsub.viewer role added as well as our existing publisher and subscriber roles

we started to refactor but realize there are several shortcomings to our current approach

#### The Correct Solution (Least Privilege)
To follow the principle of least privilege, we should not use the admin role. 
Instead, grant the service account two roles at the topic level:

roles/pubsub.publisher: Allows the service account to publish messages.
roles/pubsub.viewer: Allows the service account to perform the GetTopic check.

* the reliance on the service ENV variables is too brittle
* a subscriber may need to check the existence of both topic and subscription 

### Secrets
we think we identify secrets we wish we access as ENV variables but we don't seem to create the
mappings between ENV and secrets in our deployed services

## Review 

can you review the provided files, identify problems and plan a refactor
