# ADR 0003: Shared logical resources across local and Fleet profiles

Status: Proposed. Date: 2026-09-22. Owners: Norn contracts/clients; norn-fleet provisioning.

## Context

Local operation benefits from consolidation, while Fleet needs independent maintenance and failure boundaries. Current InfraSpec PG declarations identify a database name but lack a cluster/engine binding shared by runtime and backup tooling.

## Recommended decision

Use the same logical resource IDs and application contracts in both profiles. Upgrade the existing Mini to v3 while retaining its PostgreSQL control store, runtime and workloads; local consolidation can resolve control and app PG databases to separate users/databases on one server. Fresh HA DigitalOcean Fleet uses three etcd members on control nodes, with application databases independently provisioned. Optional Fleet control PG and local etcd are not initial GA requirements. Control backend selection never implicitly changes application database bindings.

Introduce a versioned database-service resource describing purpose, engine/version, provider reference, secret/TLS references, topology and recovery policy. InfraSpec binds logical uses to databases/users on that service. One resolver supplies runtime, migration, snapshot, restore and health tooling. Connections carry a generation so consumers can prove a completed cutover. Credentials never appear in exported topology or API status.

Resource definitions belong to infrastructure intent; apps reference them. Keep resource provisioning in the protected Fleet runner. Specify new syntax in a separate proposal before changing parsers; current Fleet v1 cannot silently acquire unsupported fields. Legacy PG declarations resolve through an explicit legacy default with visible migration guidance.

Application MySQL support is needed for ordinary WordPress. PG and MySQL get distinct adapters; do not imply PG wire compatibility provides CockroachDB recovery compatibility. CockroachDB orchestration is deferred.

## Alternatives and consequences

- Raw DSNs per app work but leave backup and migration targets ambiguous and make cutover auditing difficult.
- A Fleet PG control backend would add a supported topology and testing burden. Defer its qualification unless a concrete requirement emerges; the initial Fleet control store is etcd.
- Mandatory separate processes locally add overhead without host-level HA. Logical separation keeps contracts portable while exposing the local shared failure boundary.

## Invariants and failure behavior

A database binding identifies exactly one writable target generation. An app rollout must not split writers across old/new independent clusters. Reject unknown engine capabilities, missing secret references and inconsistent migration/backup targets. Readiness must use provider-aware proof where managed PG hides archive/replication internals. Existing advisory locks require direct connections or proven session pooling, not transaction pooling.

Local profile explicitly reports non-HA. Three VMs in one region do not establish multi-region disaster recovery. Local-to-Fleet promotion must also transfer app data, files, secrets and endpoints; moving control records alone is insufficient.

## Migration and acceptance

Introduce the resolver while retaining legacy behavior, expose resolved identities without credentials, then adopt named bindings per app. Provision targets and rehearse restores before a writer-fenced migration. Preserve original resource identities or provide explicit mappings. Rollback before writes differs from rollback after target activation; see ADR 0006.

Acceptance: identical binding resolution across runtime/migration/backup/restore; Mini upgrade retaining workload/runtime identities; fresh etcd Fleet integration; credential rotation; stale-generation rejection; managed provider evidence; PG and MySQL restore and reconnect tests. Rehearse one Mini application moving to Fleet without moving the Mini control store. Measure retained Mini control PG resource use rather than promise a footprint from service count.

## Open decisions

Choose exact schema/API versions after compatibility review. Size/price managed resources from P0 measurements. Confirm MySQL adapter scope for initial v3 and whether provisioning plus migration are GA together or separately advertised capabilities.
