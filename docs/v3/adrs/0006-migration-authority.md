# ADR 0006: Fenced authority for database and control-store migration

Status: Accepted (architecture), 2026-09-22. Accepted: 2026-09-23. No migration authorized; trust-root/RPO/RTO choices pending.

## Disposition (2026-09-23)

The safety architecture is accepted: an externally supervised migration coordinator with a durable signed manifest and checkpoints stored outside both candidate stores, one authoritative writer generation at a time, freeze-fence-transfer-verify-activate ordering, epoch-reacquired execution ownership, and dump/restore as the baseline transfer with online replication qualified separately per engine/provider. Initial GA scope stands as written — Mini upgrade retaining control PG, fresh etcd Fleet bootstrap, supported application-DB cutovers and one Mini→Fleet application rehearsal — with general PG↔etcd control conversion deferred. Left open: coordinator hosting/ownership, manifest storage and trust roots, per-consumer/provider fencing mechanisms, consistency-group syntax and numerical RPO/RTO, all gated on design spikes and M0 measurement rather than this review.

## Context

Control-store migration cannot rely exclusively on the source control API or database that it takes offline. Application database cutover must include workers, cron, integrations and connection pools as well as web processes. Old and new writable copies would diverge even if both report healthy. See the [v3 migration roadmap](../architecture-roadmap.md#p2--small-control-pg-and-named-application-database-services).

## Recommended decision

Initial GA covers the Mini upgrade while retaining control PG, fresh etcd Fleet bootstrap, supported application database upgrade/cutover paths and one Mini-to-Fleet application migration rehearsal. General PG↔etcd control-store conversion is deferred. The rules below define its future safety contract as well as the fencing needed by in-scope application migrations; they do not make every conversion path a release gate.

Run migration through an externally supervised coordinator with a durable, signed manifest and checkpoints outside both candidate stores. Record identities, export version, evidence references, writer inventory, authority generation, integrity results and recovery instructions. Store credential references rather than secrets in the manifest. Its supervisor and recovery path must work without the Norn queue.

Use one authoritative writer generation at a time. Prepare and verify a passive target, freeze mutation admission, drain or reconcile accepted work, fence every source writer, perform final transfer and integrity checks, start one target candidate with mutation admission disabled, then explicitly activate the new generation and resume consumers. Reacquire executor ownership under the new epoch; copied leases confer no authority.

Use dump/restore for small PG recovery fixtures and application transfers where the accepted downtime budget permits it. The Mini upgrade retains its current control database. Qualify online replication separately for each supported application source/target engine and provider pair, including DDL, sequences, roles, extensions and unsupported data types. Treat app databases and their writers as explicit consistency groups. Mini-to-Fleet application migration transfers the selected workload's data, files, secrets, jobs and traffic; it does not transfer control authority or require importing the Mini's entire control history.

## Alternatives and tradeoffs

- Uncoordinated dual writes or rolling DSN changes permit diverging authority and are rejected.
- Database-native replication can shorten transfer interruption but does not eliminate the final writer fence or client reconnection requirements.
- A source-resident coordinator is convenient but cannot be the sole recovery authority during source failure.

## Invariants and failure behavior

- Target activation requires verified source fencing and complete final transfer. Unknown fence or checkpoint outcomes stop activation for reconciliation.
- Lease expiry alone does not stop external side effects. Require execution-boundary fencing or proof that the previous executor stopped before takeover.
- No accepted mutation, unexpired idempotency record, revocation, recovery lineage or signed receipt can silently disappear during export/import.
- A lost response is an ambiguous result to reconcile by operation identity, not permission to repeat an external action.
- Keep the old store fenced after activation. Before any target writes, coordinated return to the source is possible; after target writes, return requires reverse transfer/reconciliation or an explicitly accepted loss boundary.
- Missing archive or signing material blocks verification; preserve original evidence bytes and independent key backups.

## Migration

Define versioned recovery exports and a checkpoint state machine first. Initial rehearsals cover independent control backup/restore for PG and etcd, the Mini's same-PG product upgrade, supported application engine upgrade/cutover pairs and one Mini-to-Fleet application. Compare passive target reads before database cutover. Preserve logical identities and publish any mapping. Retain the source through the agreed acceptance window; retirement is a separate, explicit step after restore and workload checks. PG→etcd, etcd→PG and whole-control-plane relocation remain future capabilities requiring their own acceptance evidence.

## Acceptance evidence

For each supported cutover path, interrupt every checkpoint and recover using the manifest without the source API. Test stale writers/executors, ambiguous responses, target validation failure and consumer generation mismatch. Exercise the published post-write recovery boundary: reverse migration only when that path is supported, otherwise forward repair or restore with explicitly declared loss bounds. Verify constraints/checksums, identities, auth, receipts and acknowledged work as applicable. Demonstrate fresh etcd operation, archive retrieval and full restore with no control PG dependency. Measure mutation pause, write interruption and recovery time against per-path budgets. Deferred general control-store conversion and reverse conversion are not initial GA gates.

## Unresolved decisions

Choose coordinator hosting/ownership, manifest storage and trust roots, source fencing mechanisms per consumer/provider, consistency-group syntax, retention/retirement windows and numerical RPO/RTO. Define external systems that cannot enforce fencing and their manual reconciliation requirements before enabling automatic takeover. Availability promises remain conditional on each engine/provider path passing rehearsal.
