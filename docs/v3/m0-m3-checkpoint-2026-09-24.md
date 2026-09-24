# M0–M3 integration checkpoint — 2026-09-24

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
consumers and PG-free etcd startup remain open. The Mini's observed schema
does not yet migrate from its unversioned state without an adoption repair;
that work is under separate review. **No M0–M3 milestone is signed off and no
v3 deployment is implied by these merges.**

The next review order is: finish the guarded Mini schema adoption and private
restore evidence; convert the remaining M1 external effects and remove
concrete PostgreSQL control consumers; qualify M2 retention, database
bindings, and representative recovery; then finish M3 backend-neutral runtime
and three-member etcd bootstrap, fault, restore, and soak testing. A loaded
M4 capacity exercise depends on those control contracts.

This updates the [2026-09-23 checkpoint](m0-m3-checkpoint-2026-09-23.md) after M2 PR [#51](https://github.com/antiartificial/norn/pull/51) and M3 PR [#50](https://github.com/antiartificial/norn/pull/50) merged into `feature/norn-v3-planning-handoff`. It records code integration, not milestone exit or release qualification. The v3 feature branch has not been deployed to Mini or Fleet.

| Milestone | Added evidence | Remaining exit gate |
| --- | --- | --- |
| M0 | [Mini control-store measurements](m0-mini-measurements-2026-09-24.md) bind the running binary to an exact signed release/source SHA and record active PostgreSQL identity, schema-only dump hash, bytes, and two short-interval samples. A [topology comparison](m0-mini-topology-2026-09-24.md) records app, manifest, ingress counts, and unresolved duplicate/inactive cases. The [decision register](decision-register-2026-09-24.md) reconciles accepted ADR 0007 with six proposed ADRs. | Prove schema migration/version mapping; measure representative growth; map jobs/routes/volumes/database owners; make sanitized CI and isolated private restore fixtures; review proposed ADRs, owners, and numeric budgets. |
| M1 | Signed acceptance, fencing, and auth boundaries remain integrated. The [external-effect audit](m1-control-boundary-audit.md) still identifies inline effectful paths. | Convert each live external effect to a durable accepted/reserved/reconciled execution path; qualify two-replica races and old-data compatibility; remove concrete PostgreSQL consumers. The app-restart candidate is isolated because it would accept a request only to fail it without executing a restart. |
| M2 | Fleet GitHub PR/apply reserves a signed plan-scoped intent and archive capacity **before** external dispatch. A separately signed completion binds the operation, plan, status, and GitHub result; archive verification checks the exact exposed payload. [PR #52](https://github.com/antiartificial/norn/pull/52) adds operator reconciliation of queued reservations through signed acceptance and idempotent verified no-write completion. Focused PostgreSQL acceptance and archive tests passed. | Bound hot receipt/identity lifetime and byte reserve; cover remaining non-saga domains; qualify live GitHub crash boundaries, MySQL, Nomad, object service, growth, and restore. |
| M3 | Etcd operation claims use server leases, generation-fenced mutations, a running index, and paginated recovery. [PR #53](https://github.com/antiartificial/norn/pull/53) adds leased app locks and atomic lock-fence comparison on success, defer, retry, and failed terminalization. Local live-etcd race and recovery tests and repository CI passed. | Drain or explicitly migrate pre-lease etcd running records before mixed-version rollout. Implement checkpoint/effect aggregates, backend-neutral consumers, and PG-free startup; qualify TLS three-member Fleet, quorum faults, restore, and soak. |

The M2/M3 safety and reconciliation slices are merged; **no M0–M3 milestone is signed off**. The next dependency order is: finish M1 external-effect paths and concrete-store removal; close M2 retention and Mini recovery qualification; then complete M3 PG-free wiring and three-member Fleet qualification. M4 loaded capacity proof depends on those runtime contracts.

[PR #54](https://github.com/antiartificial/norn/pull/54) also merged the first M4 foundation: signed, region-scoped app scale operations and durable desired-replica intent consumed by deploy and rollback. A PostgreSQL lock-wait test rejects expired claims; the full API suite passed against disposable PostgreSQL with the known Darwin host-metrics sample skipped. This does not establish loaded 2→3→2 placement, drain, or replacement behavior.

Focused package and repository CI checks passed for both PRs. The merged branch passed `go test ./... -skip '^TestSampleDarwinHostMetrics$' -count=1 -p 1` from `v2/api` on 2026-09-24. The exact Darwin host-metrics sampler exclusion is a known local test-environment issue. Local etcd test members were stopped after verification. No live Norn deployment or provider mutation was performed.
