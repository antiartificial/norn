# M0–M3 integration checkpoint — 2026-09-24

## Current integration update

PR [#67](https://github.com/antiartificial/norn/pull/67) added versioned
operation replay expiry; it does not release every retained hot signed-identity
or byte reservation. PR [#68](https://github.com/antiartificial/norn/pull/68)
added a narrow etcd source-validation process that starts without control PG;
normal Fleet consumers are not yet backend-neutral. PR
[#69](https://github.com/antiartificial/norn/pull/69) integrated supervised
`app.snapshot`, including bounded artifact admission, signed terminal-result
replay, and durable publication intent/receipt. Its local PostgreSQL and Linux
cgroup tests passed; a deployed Mini/Fleet runner and restore qualification
remain open. PR [#70](https://github.com/antiartificial/norn/pull/70) recorded
the exact Mini source-to-schema mapping through candidate migration 16.

A subsequent [private-data migration-17 rehearsal](m0-mini-private-restore-migration17-rehearsal-2026-09-24.md)
restored a copied Mini control database into an isolated PostgreSQL 17
container, applied migrations 1–17, and preserved all 28 legacy table counts
and primary-key fingerprints. The second migrator invocation applied no
versions and did not change compatibility metadata. It did not start either
API, exercise rollback, or verify app/job/route/volume/database ownership. **No M0–M3
milestone gate is signed off, and no v3 code was deployed to Mini or Fleet.**

## Further integration update

PRs [#62](https://github.com/antiartificial/norn/pull/62),
[#63](https://github.com/antiartificial/norn/pull/63), and
[#64](https://github.com/antiartificial/norn/pull/64) merged into the v3
integration branch. A source-validation-only preflight now completes signed
acceptance, an etcd claim and app lock, a claim-fenced source checkpoint, and
terminalization on real etcd without PostgreSQL. The normal `norn-api`
startup, handler, and Fleet consumers still require PostgreSQL; this is not a
PG-free Fleet runtime. A dedicated snapshot helper now attests the exact
`pg_dump` binary and dump artifact, binds its manifest to the accepted target
descriptor, and removes temporary connection files. Manual `app.snapshot`
does not yet use that helper or the durable effect ledger. The M2 reserve can
optionally cap exact retained signed acceptance payload bytes atomically; it
does not bound all hot control data, and its reservations cannot be released
until signed acceptance has an expiry and prune contract.

The [Mini size measurement](m0-mini-measurements-2026-09-24.md) now includes a
third read-only sample at 18:02 UTC. It is still a short observation. No
M0–M3 milestone is signed off, and none of these merges deployed v3 to Mini
or Fleet.

## Later integration update

PRs [#55](https://github.com/antiartificial/norn/pull/55),
[#56](https://github.com/antiartificial/norn/pull/56),
[#57](https://github.com/antiartificial/norn/pull/57), and
[#58](https://github.com/antiartificial/norn/pull/58) subsequently merged into
`feature/norn-v3-planning-handoff`. Legacy snapshot restore and confirmed
pruning, canary promotion, app restart, and wake-gateway scaling now accept
signed operations for worker execution. Restart records per-allocation stop
attempts; unresolved app effects exclude other app effect reservations until they are
reconciled. Expired restart and canary claims requeue for that reconciliation.
Wake requests coalesce by a durable cycle number and can wake again after an
explicit scale to zero.

These changes close four paths in the historical [M1 external-effect audit](m1-control-boundary-audit.md).
Cron, forge, ContextDB rollback, function execution, and other effect paths
still need durable boundaries. Snapshot restore still needs a crash and
lease-loss rehearsal around its worker subprocess. Concrete PostgreSQL
consumers and PG-free etcd startup remain open. At this checkpoint, the Mini's
observed schema still required an unversioned adoption repair. **No M0–M3 milestone is signed off and no
v3 deployment is implied by these merges.**

PR [#60](https://github.com/antiartificial/norn/pull/60) later merged that
guarded Mini adoption path. A pinned fingerprint of all 28 pre-v1 tables and
the control-events sequence matched read-only live Mini PostgreSQL 17 and a
private schema-only restore on disposable PostgreSQL 16. The restored schema
migrated through version 13 without changing migration 1's checksum, and
control-recovery inspection passed. The adoption transaction blocks legacy
Fleet dispatch inserts and refuses any existing dispatch rows. The full API
suite passed against a fresh disposable PostgreSQL database with the known
local Darwin host-metrics sampler excluded; repository CI passed. This is a
schema-only rehearsal. Representative data/rollback rehearsal, old-worker
compatibility, growth budgets, and app/route/volume ownership remain M0/M5
exit work. No Mini database was changed.

The next review order is: complete private rollback and mixed-version
evidence; convert the remaining M1 external effects and remove
concrete PostgreSQL control consumers; qualify M2 retention, database
bindings, and representative recovery; then finish M3 backend-neutral runtime
and three-member etcd bootstrap, fault, restore, and soak testing. A loaded
M4 capacity exercise depends on those control contracts.

This updates the [2026-09-23 checkpoint](m0-m3-checkpoint-2026-09-23.md) after M2 PR [#51](https://github.com/antiartificial/norn/pull/51) and M3 PR [#50](https://github.com/antiartificial/norn/pull/50) merged into `feature/norn-v3-planning-handoff`. It records code integration, not milestone exit or release qualification. The v3 feature branch has not been deployed to Mini or Fleet.

| Milestone | Added evidence | Remaining exit gate |
| --- | --- | --- |
| M0 | [Mini control-store measurements](m0-mini-measurements-2026-09-24.md) bind the running binary to an exact signed release/source SHA and record active PostgreSQL identity, schema-only dump hash, bytes, and two short-interval samples. A [migration-17 private restore rehearsal](m0-mini-private-restore-migration17-rehearsal-2026-09-24.md) preserves all legacy row counts and primary-key fingerprints, and proves migration idempotence in a network-disabled PostgreSQL 17 target. A [topology comparison](m0-mini-topology-2026-09-24.md) records app, manifest, ingress counts, and unresolved duplicate/inactive cases. The [decision register](decision-register-2026-09-24.md) reconciles accepted ADR 0007 with six proposed ADRs. | Measure representative growth; map jobs/routes/volumes/database owners; make a retained sanitized CI fixture; rehearse rollback and mixed-version compatibility; review proposed ADRs, owners, and numeric budgets. |
| M1 | Signed acceptance, fencing, and auth boundaries remain integrated. The [external-effect audit](m1-control-boundary-audit.md) still identifies inline effectful paths. | Convert each live external effect to a durable accepted/reserved/reconciled execution path; qualify two-replica races and old-data compatibility; remove concrete PostgreSQL consumers. The app-restart candidate is isolated because it would accept a request only to fail it without executing a restart. |
| M2 | Fleet GitHub PR/apply reserves a signed plan-scoped intent and archive capacity **before** external dispatch. A separately signed completion binds the operation, plan, status, and GitHub result; archive verification checks the exact exposed payload. [PR #52](https://github.com/antiartificial/norn/pull/52) adds operator reconciliation of queued reservations through signed acceptance and idempotent verified no-write completion. Focused PostgreSQL acceptance and archive tests passed. | Bound hot receipt/identity lifetime and byte reserve; cover remaining non-saga domains; qualify live GitHub crash boundaries, MySQL, Nomad, object service, growth, and restore. |
| M3 | Etcd operation claims use server leases, generation-fenced mutations, a running index, and paginated recovery. [PR #53](https://github.com/antiartificial/norn/pull/53) adds leased app locks and atomic lock-fence comparison on success, defer, retry, and failed terminalization. Local live-etcd race and recovery tests and repository CI passed. | Drain or explicitly migrate pre-lease etcd running records before mixed-version rollout. Implement checkpoint/effect aggregates, backend-neutral consumers, and PG-free startup; qualify TLS three-member Fleet, quorum faults, restore, and soak. |

The M2/M3 safety and reconciliation slices are merged; **no M0–M3 milestone is signed off**. The next dependency order is: finish M1 external-effect paths and concrete-store removal; close M2 retention and Mini recovery qualification; then complete M3 PG-free wiring and three-member Fleet qualification. M4 loaded capacity proof depends on those runtime contracts.

[PR #54](https://github.com/antiartificial/norn/pull/54) also merged the first M4 foundation: signed, region-scoped app scale operations and durable desired-replica intent consumed by deploy and rollback. A PostgreSQL lock-wait test rejects expired claims; the full API suite passed against disposable PostgreSQL with the known Darwin host-metrics sample skipped. This does not establish loaded 2→3→2 placement, drain, or replacement behavior.

Focused package and repository CI checks passed for both PRs. The merged branch passed `go test ./... -skip '^TestSampleDarwinHostMetrics$' -count=1 -p 1` from `v2/api` on 2026-09-24. The exact Darwin host-metrics sampler exclusion is a known local test-environment issue. Local etcd test members were stopped after verification. No live Norn deployment or provider mutation was performed.

## Grouped release integration continuation

Draft [Norn PR #76](https://github.com/antiartificial/norn/pull/76) groups the
remaining M0–M3 Norn work; draft
[Fleet PR #176](https://github.com/antiartificial/norn-fleet/pull/176) groups
the host etcd bootstrap and recovery work. The separate M2 MySQL PR was merged
into #76 and closed. These are review containers, not milestone signoff.

- M0: four same-day read-only Mini size samples span about eight hours, with
  1,564,672 bytes of total database growth; this remains too short for a
  retention budget. The private migration-17 rehearsal preserved 28 legacy table counts
  and primary-key fingerprints across 245,383 rows. The read-only app-to-Nomad
  join resolved 18 names and left eight unresolved. Route and database owners,
  a fixture covering representative Mini topology, representative growth, rollback, and mixed-version
  evidence remain open.
  A synthetic, private-data-free fixture now checks selected legacy rows
  through migrations 1–17 and the reader-version refusal on PostgreSQL 17.7;
  it runs in a dedicated PR CI job. Installed-binary rollback and full Mini
  workload compatibility remain open.
- M1: the etcd canary effect adapter now atomically reserves under a live
  operation claim and per-app gate, records launch and completion, and supports
  repeat-safe resolution and recovery. Its tests ran against disposable etcd
  v3.5.17. The handler's managed-token actor lookup now uses the AuthStore
  lineage contract on both PostgreSQL and etcd instead of querying PostgreSQL
  from the handler. Replay expiry holds accepted identities until their effects are
  terminal, and an opt-in canary-only worker reconciled a lost Nomad response
  with one external PUT against fake Nomad. The normal etcd router now mounts
  public canary admission only with both the worker and HTTP preview flags.
  A disposable-etcd HTTP-to-worker test covered signed admission, token-rotation
  replay, conflicting intent, and one Nomad promotion against fake Nomad.
  A separate local Nomad 2.0.7 and etcd 3.5.17 test promoted the exact real
  healthy canary deployment. An earlier 0/1-healthy attempt exposed a gap:
  Nomad rejected promotion and the accepted operation stayed pending. Admission
  now rejects an unready canary before durable acceptance, allowing the same
  key to be retried after health. The worker also rechecks the exact deployment
  before reserving and before sending the Nomad promotion. A crash after
  reservation but before a confirmed remote effect can still remain pending
  conservatively; that window, process-crash, and three-member etcd fault
  qualification remain open before enabling the preview for release; see
  [the preview gate](etcd-canary-preview.md). ContextDB feedback rollback now
  fails closed with HTTP 501 and is unavailable in the Ops UI because the
  remote idempotency contract is absent; restoring it requires the
  [cross-service contract](m1-contextdb-feedback-rollback-contract.md).
  A disposable Nomad test also showed that periodic force ignores the generic
  idempotency token: repeated calls created distinct evaluations and children.
  Cron trigger therefore remains an inline-effect conversion gate; see
  [the force qualification](m1-cron-force-idempotency-qualification-2026-09-24.md).
  The Nomad adapter now has a version-gated atomic resume of a stopped periodic
  parent with an effect marker for recovery. The HTTP resume path still uses
  inline resubmission; signed intent, claim-fenced execution, and reconciliation
  remain required before this primitive closes that route's M1 gate.
  A disposable Nomad 2.0.7 test proved pause, stale-revision refusal, and
  resume against the real API. It exposed a nested agent-version response that
  had made the existing pause CAS guard reject supported servers; version
  detection now reads Nomad's actual response shape.
  A replacement-resume adapter now CAS-registers a freshly built periodic job
  with an effect marker. A second disposable Nomad run proved schedule
  replacement and stale-revision refusal. The durable worker still needs to
  rebuild the exact image, secrets, and database delivery at launch without
  persisting plaintext material in the accepted operation.
- M2: local MySQL runtime and verified-TLS health are implemented. Generated
  service, periodic, and function jobs delivered exact component bytes inside
  allocations on pinned Nomad 1.9.7. A WordPress image PHP client also
  connected to disposable MySQL 8.4.11, verified database/account identity,
  and wrote/read a temporary table; see
  [the allocation record](m2-nomad-197-allocation-delivery-2026-09-24.md).
  Completed WordPress installation, TLS application runtime, backup/restore, retention
  reserve, and representative recovery remain open.
  A separate opt-in disposable test now passes the exact four runtime
  components to pinned, unmodified `wordpress:6.8.2-php8.3-apache` against
  `mysql:8.4` and verifies the WordPress HTTP installation page. This closes
  ordinary image-level startup compatibility; TLS application runtime,
  delivery through a Nomad allocation, and recovery remain open.
  A disposable MySQL 8.4 rehearsal now resolves exact source/target bindings,
  rejects stale generations, and verifies a `mysqldump` artifact restores to
  the distinct target while source data remains intact. Snapshot and restore
  capabilities stay gated until a durable recovery lifecycle is implemented;
  see [the rehearsal](m2-mysql-dump-restore-rehearsal-2026-09-24.md).
- M3: normal etcd startup and managed-token lifecycle have a narrow PG-free
  Fleet router. Passive candidate health/schema now rechecks etcd after startup
  and a real API process passed with a poisoned PostgreSQL URL. Full app
  consumers, three-member host bootstrap and restore,
  host fault behavior, and soak qualification remain open. The host PR's CI
  contract job was blocked by GitHub account billing status during this
  checkpoint; that is not a passing host qualification.
  A disposable three-member TLS/RBAC cluster now passed the normal API process
  test with absent and poisoned PostgreSQL DSNs, one-member loss, quorum-loss
  refusal, and full snapshot restore. This narrows the local transport and
  recovery gap; host-supervised Fleet bootstrap, partition/disk faults,
  certificate rotation, and live restore still require qualification. See
  [the three-member record](m3-three-member-disposable-qualification.md).

No v3 deployment to Mini or Fleet is claimed. M0–M3 still need their exit
gates before the loaded M4 capacity exercise can rely on these contracts.
