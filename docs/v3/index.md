---
layout: home
hero:
  name: Norn 3.0.0
  text: A control plane for Fleet workloads
  tagline: Upgrade the platform while workloads keep serving. Keep infrastructure authority, application state, and recovery evidence explicit.
  actions:
    - theme: brand
      text: Start with V3
      link: /v3/guide/getting-started
    - theme: alt
      text: Architecture
      link: /v3/architecture/overview
    - theme: alt
      text: Release evidence
      link: /v3/release-program-milestones
features:
  - title: Online platform upgrades
    details: The DigitalOcean pilot upgraded three controls through a controller, with independent workload observation and retained terminal evidence.
    link: /v3/operations/online-upgrades
  - title: Separate operational authority
    details: Management and its runner remain outside the workload Fleet so they can observe, recover, and retire it.
    link: /v3/architecture/overview
  - title: Explicit capability status
    details: Distinguish the tested platform upgrade from fresh creation, controller workload admission, and resizing that still need qualification.
    link: /v3/reference/capabilities
  - title: Recoverable operations
    details: Bind intents to exact sources and identities. Read back ambiguous effects before deciding whether another action is safe.
    link: /v3/operations/recovery
---

## The first Fleet release

Norn 3.0.0 names the Fleet milestone completed on 2026-11-16. The staging
pilot passed its live online upgrade, then was retired with management retained.
The signed artifact remains identified by its full source SHA. The final 3.0.0
tag has not been published. See [version identity](reference/versioning.md).

These pages describe the current architecture and its qualification limits.
The [planning records](README.md), [launch state](LAUNCH-STATE.md), and
[release milestones](release-program-milestones.md) retain decisions and evidence.

[V2 documentation](../v2/index.md) and [V1 documentation](../v1/guide/getting-started.md)
remain available. Versioned HTTP routes and source directory names have their
own meanings; `/api/v1` and `v2/` do not identify the product release.
