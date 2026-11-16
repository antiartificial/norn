# Why Norn

Running an application is more than keeping its container alive. You need to
know what is deployed, whether a change is ready, where traffic goes, and what
to do when a deployment or platform upgrade stops halfway through.

Norn brings those tasks into one application control plane. An `infraspec.yaml`
describes the app. The API, CLI, and dashboard expose its services, dependencies,
health, deployment history, and recovery work. Nomad schedules the workloads,
Consul tracks service health, and ingress routes traffic to ready allocations.

## A repeatable workflow

**Discover.** See applications, services, allocations, endpoints, missing
secrets, and operational events in one place. A running process and a responding
application are different facts; Norn exposes both runtime and service evidence.

**Rehearse.** Validate an app and run preflight before deployment. Build and
test the candidate, check required inputs, and find preparation failures before
touching the serving workload. Preflight can consume build resources and contact
dependencies. It does not prove that a later live change will succeed.

**Deploy.** Follow an application change through source preparation, build,
tests, configured snapshots and migrations, scheduler submission, readiness,
and routing. Stage records show where the change succeeded or failed.

**Drain.** Inspect active work before platform maintenance or node removal.
Drain is an explicit readiness gate, not a reason to kill an unknown operation.
Fleet capacity changes also need placement and remaining-capacity evidence.

**Recover.** Find the original operation and its last proven effect. Follow
its receipt and readback instead of retrying an ambiguous mutation as new work.
Snapshots, rollback, and platform recovery have different data boundaries.

## One application definition

An InfraSpec can describe HTTP services, workers, scheduled jobs, and functions,
with resources, secrets, health probes, and dependency bindings. The same app
identity connects its deployment history, logs, events, and recovery actions.

Your repository remains the source of application code. Norn adds an operational
record around each change. Use immutable pushed commit SHAs for repeatable
releases and keep new application drafts disabled until reviewed.

## Operate the platform as well as its workloads

Fleet adds explicit infrastructure ownership and controller-managed platform
updates. Provider credentials stay in protected infrastructure execution.
Norn tracks the intended source, target, progress, and evidence. Management can
remain available independently from the workload Fleet it operates.

The V3 pilot proved a live Norn control-plane upgrade while an existing workload
continued responding. That is useful for iterating on the platform without
rebuilding the environment for every change. See the [capability reference](../reference/capabilities.md)
for what was qualified and what still needs proof.

## Choose the amount of platform you need

Use **self-hosted** for a lighter single-host experience with local runtime
integration and its own recovery lifecycle. Use **Fleet** for multiple nodes,
separate management, control-store quorum, and infrastructure operations.
Both use application and control contracts; their upgrade procedures differ.

DigitalOcean is the currently implemented and qualified provider adapter.
The architecture separates provider effects from application control, but other
providers need their own adapters and qualification. Multi-provider support is
not part of the 3.0.0 claim.

[Start with Norn](getting-started.md), explore the [architecture](../architecture/overview.md),
or follow the [operator workflow](../operations/workflow.md).
