---
layout: home
hero:
  name: Norn 3.0.0
  text: Discover, deploy, and operate your applications
  tagline: One application definition. A visible path from preflight to service readiness. Self-hosted or Fleet, with explicit recovery when changes go wrong.
  actions:
    - theme: brand
      text: Get started
      link: /v3/guide/getting-started
    - theme: alt
      text: Why Norn
      link: /v3/guide/why-norn
    - theme: alt
      text: Explore the architecture
      link: /v3/architecture/overview
features:
  - title: Discover
    details: See app health, services, endpoints, allocations, secrets status, and operational events in one control plane.
    link: /v3/operations/workflow
  - title: Rehearse
    details: Prepare and test a candidate before changing the serving workload. Catch build failures and missing inputs early.
    link: /v3/operations/workflow#validate-and-rehearse-a-candidate
  - title: Deploy
    details: Follow build, configured data changes, scheduler submission, readiness, and routing through recorded deployment stages.
    link: /v3/operations/workflow#deploy-and-verify-service
  - title: Drain
    details: Inspect active work and capacity before maintenance. Keep platform updates and workload changes explicit.
    link: /v3/operations/workflow#drain-before-maintenance
  - title: Recover
    details: Use the original operation, snapshots, and effect readback to resolve failures without blindly repeating mutations.
    link: /v3/operations/recovery
  - title: Operate a Fleet
    details: Keep provider authority in protected execution and upgrade Norn controls while observing workload continuity.
    link: /v3/operations/fleet
---

## Your application, with an operational history

Norn connects an `infraspec.yaml` to application discovery, source and build,
health checks, secrets, deployment progress, and recovery. The API, CLI, and
dashboard let you inspect what is serving and what a change actually did.
Nomad schedules the workloads, Consul tracks discovery and health, and ingress
routes requests to application allocations.

Start with [why Norn](/v3/guide/why-norn), define an
[application](/v3/guide/applications), or follow the
[operator workflow](/v3/operations/workflow).

## Two release strategies

Choose **self-hosted** for a lighter single-host experience with local runtime
integration. Choose **Fleet** for multiple nodes, separate management and
controller-driven platform operations. They share application/control concepts
and have distinct deployment and recovery procedures.

DigitalOcean is the current provider adapter, not the product boundary.
The architecture separates provider effects; other adapters are future work.
See [capability status](/v3/reference/capabilities) for the difference between
implemented behavior and live-qualified operations.

## What V3 establishes

The first Fleet milestone proved a controller-managed online Norn upgrade while
an existing workload continued responding. The [architecture](/v3/architecture/overview)
explains the design; [release evidence](/v3/release-program-milestones) records
that qualification and its limits. Version tags and signed deployment artifacts
have separate [identities](/v3/reference/versioning).

[V2 documentation](/v2/) and [V1 documentation](/v1/guide/getting-started)
remain available. Current source lives under `v2/`; that path and `/api/v1`
identify code layout and an API contract, not the product version.
