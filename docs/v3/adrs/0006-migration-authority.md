# ADR 0006: Fenced authority for database and control-store migration

Status: proposed, revised 2026-09-30. Planning only; no migration authorized.

## Context

Control-store migration cannot rely exclusively on the source control API or database that it takes offline. Application database cutover must include workers, cron, integrations and connection pools as well as web processes. Old and new writable copies would diverge even if both report healthy. See the [v3 migration roadmap](../architecture-roadmap.md#p2--small-control-pg-and-named-application-database-services).

## Recommended decision

Initial GA covers the Mini upgrade while retaining control PG, fresh etcd Fleet bootstrap, supported application database upgrade/cutover paths and one Mini-to-Fleet application migration rehearsal. General PG↔etcd control-store conversion is deferred. The rules below define its future safety contract as well as the fencing needed by in-scope application migrations; they do not make every conversion path a release gate.

Run migration through a Fleet-owned external supervisor. Its authority log is
an append-only sequence of immutable, signed, hash-chained phase manifests
stored outside the source application database, target application database,
Mini control PostgreSQL and Fleet control etcd. The supervisor and its recovery
path must work without the Norn queue or either candidate control API. A
projection in a Norn control store may accelerate admission and status reads,
but it is not the recovery authority and cannot override the external log.

Each manifest binds the migration and accepted operation IDs, app and logical
database, exact source and target service/binding identities and generations,
source and target control-store identities and authority epochs, candidate
release digest, active catalog revision and digest, complete writer-inventory
digest, consistency-group digest, authority generation, phase, prior-manifest
digest, evidence object identity and digest, signer key ID, expiry, and declared
recovery policy. Record credential references rather than DSNs, tokens or key
material. Canonical bytes and schema version are part of the signed contract.

Use a dedicated migration-evidence signing role. Fleet's protected execution
environment holds its private key; Norn and independently retained recovery
material contain only the allowlisted public trust roots and key IDs. This role
is separate from release signing, provider credentials and database
maintenance credentials. Key rotation retains every public key needed to
verify an unretired migration chain. Missing or revoked verification material
fails closed.

Norn verifies a phase by checking the complete signature and hash chain,
manifest identity and expiry, retained immutable evidence bytes, and typed
phase-specific assertions. A digest proves byte identity, not that an external
effect occurred. Where a fact can change, Norn or a dedicated verifier performs
a fresh, narrow readback against the exact source, target, consumer generation
or route identity named by the manifest. Fleet obtains short-lived,
migration- and phase-scoped Norn credentials through its protected workload
identity. Norn does not receive Fleet's provider token, remote-state
credentials, database maintenance credentials, traffic-publisher credentials
or the evidence signing private key.

Use one authoritative writer generation at a time. The migration is a fenced
saga, not a distributed transaction: PostgreSQL transactions, etcd
transactions, Nomad changes and traffic publication cannot commit atomically.
The signed authority log establishes the recovery point after a lost response;
each system still performs its own compare-and-swap or effect readback.

Advance through these ordered boundaries:

1. **Prepare:** keep the source authoritative; restore and verify a passive
   target with mutation admission disabled.
2. **Quiesce:** freeze application mutation and deployment admission, disable
   schedules and queue intake, drain or reconcile acknowledged work, then
   fence every source writer and read back the fence. An unknown writer blocks
   advancement.
3. **Final sync:** transfer the final source checkpoint and verify application
   integrity, accepted work, files and other consistency-group members. An
   ambiguous transfer is reconciled by its recorded identity before retry.
4. **Activate:** after verified source fencing and final sync, install exactly
   one new consumer generation with generation-bound target credential
   references while target mutation admission remains disabled. Start the
   target consumers and prove their target and generation. Publish traffic
   through the revisioned route authority in
   [ADR 0008](0008-fleet-app-route-authority.md), then open target mutation
   admission. A route observation or DNS response alone cannot activate a
   database generation.
5. **Verify and accept:** prove that web, worker, scheduled and external
   consumers use only the target; old credentials and paths remain fenced;
   and data and acknowledged work reconcile. Record operator acceptance after
   the selected observation window. Retirement is a later operation.

Reacquire executor ownership under the new epoch at every recovery boundary;
copied leases confer no authority. Stale consumer generations must be rejected
at Norn admission and at the applicable credential, network or database
boundary rather than merely omitted from a new deployment.

Use dump/restore for small PG recovery fixtures and application transfers where the accepted downtime budget permits it. The Mini upgrade retains its current control database. Qualify online replication separately for each supported application source/target engine and provider pair, including DDL, sequences, roles, extensions and unsupported data types. Treat app databases and their writers as explicit consistency groups. Mini-to-Fleet application migration transfers the selected workload's data, files, secrets, jobs and traffic; it does not transfer control authority or require importing the Mini's entire control history.

## Alternatives and tradeoffs

- Uncoordinated dual writes or rolling DSN changes permit diverging authority and are rejected.
- Database-native replication can shorten transfer interruption but does not eliminate the final writer fence or client reconnection requirements.
- A source-resident coordinator is convenient but cannot be the sole recovery authority during source failure.
- A journal stored only in Mini PostgreSQL or Fleet etcd would make recovery
  depend on one side of a cross-store transition and is rejected.
- A shared-secret MAC can protect a node-local supervisor channel, but it would
  require every independent verifier to possess signing authority. Use
  asymmetric signatures for the portable authority and evidence chain.

## Invariants and failure behavior

- Target activation requires verified source fencing and complete final transfer. Unknown fence or checkpoint outcomes stop activation for reconciliation.
- Lease expiry alone does not stop external side effects. Require execution-boundary fencing or proof that the previous executor stopped before takeover.
- No accepted mutation, unexpired idempotency record, revocation, recovery lineage or signed receipt can silently disappear during export/import.
- A lost response is an ambiguous result to reconcile by operation identity, not permission to repeat an external action.
- Record the first accepted target write as a durable recovery boundary. Before
  that boundary, return to the source requires proof that the target accepted
  no writes, a verified target fence, restoration of the prior consumer and
  route generations, and explicit source reactivation under a new generation.
  After that boundary, switching a route or restoring old credentials is not
  rollback: return requires a separately qualified reverse transfer and
  reconciliation path. Without that path, preserve the source fence and repair
  forward under the declared loss boundary.
- Missing archive or signing material blocks verification; preserve original evidence bytes and independent key backups.

## Migration

Define versioned recovery exports and a checkpoint state machine first. Initial rehearsals cover independent control backup/restore for PG and etcd, the Mini's same-PG product upgrade, supported application engine upgrade/cutover pairs and one Mini-to-Fleet application. Compare passive target reads before database cutover. Preserve logical identities and publish any mapping. Retain the source through the agreed acceptance window; retirement is a separate, explicit step after restore and workload checks. PG→etcd, etcd→PG and whole-control-plane relocation remain future capabilities requiring their own acceptance evidence.

Implement the authority-log reader, signature and chain verifier, typed phase
schemas and control-store projections without enabling activation. Existing
guards must continue refusing a live database-target change until the selected
path has qualified source fencing, final transfer, consumer-generation and
route effects. Activation, verification and acceptance remain closed when the
external proof or fresh readback is absent, stale or ambiguous.

## Acceptance evidence

For each supported cutover path, interrupt every checkpoint and recover using the manifest without the source API. Test stale writers/executors, ambiguous responses, target validation failure and consumer generation mismatch. Exercise the published post-write recovery boundary: reverse migration only when that path is supported, otherwise forward repair or restore with explicitly declared loss bounds. Verify constraints/checksums, identities, auth, receipts and acknowledged work as applicable. Demonstrate fresh etcd operation, archive retrieval and full restore with no control PG dependency. Measure mutation pause, write interruption and recovery time against per-path budgets. Deferred general control-store conversion and reverse conversion are not initial GA gates.

## Decisions retained for each selected path

This ADR chooses Fleet ownership of the external authority log, the signed
hash-chained manifest and separated trust root, the fenced saga order, one
consumer generation, and the pre-write/post-write recovery boundary. It does
not select or qualify an application or provider pair.

Before enabling activation, owners must select the first synthetic application
and source/target provider pair and record:

- the complete consistency group and every web, worker, scheduled, queued,
  pooled and external writer;
- a provider-enforceable fence and independent readback for every writer;
- engine versions, extensions, roles, backup/restore and final-transfer
  behavior, including retry and ambiguous-result reconciliation;
- file and secret transfer, generation-bound credential issuance and
  revocation, and the exact traffic publisher covered by ADR 0008;
- the immutable archive, object-retention policy, signing service, key backup
  and key-rotation procedure;
- measured mutation pause, RPO/RTO, observation and source-retention windows,
  and either a qualified reverse path or an explicit forward-repair/loss
  boundary; and
- manual reconciliation for every external system that cannot enforce a
  generation or fencing token.

Availability promises and automatic takeover remain disabled until the exact
engine, provider and application path passes interruption and recovery
rehearsals. Accepting this ADR would approve the architecture contract, not a
production migration, credential grant or activation path.
