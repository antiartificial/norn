# M1–M3 integration ledger

Integration base: `feature/norn-v3-planning-handoff` at `ab22961`.
Source of selected adapters and conformance tests: `feature/durable-app-recovery-ui` at `2102c24`.
This ledger tracks code integration separately from milestone qualification. It does not authorize a live Mini change or a provider apply.

## Canonical contract decisions

| Domain | Canonical contract | Durable work to adapt | Unsafe shortcut to reject |
| --- | --- | --- | --- |
| Work acceptance | `store.OperationStore` in `operation_acceptance_types.go`: authority, signed atomic accept, resolve | Shared conformance structure and backend-neutral request identity tests | Direct insertion of claimable work or a bare-ID `OperationStore` |
| Execution ownership | `store.ExecutionStore` and immutable `OperationClaim` in `operations.go` | Etcd operation storage and recovery tests | Finish, retry, defer, or effect launch by operation ID without owner and generation |
| External effects | `effect` reservation and supervisor execution identity bound to the operation claim | Etcd persistence and cross-backend tests | Replaying an ambiguous external effect without durable proof |
| Exec sessions and auth | v3 owner-token session lease, with atomic credential/device revocation and cancellation | Durable auth aggregate and its conformance cases | Separate authorization read before renewal or reverting to owner-ID-only sessions |
| Application databases | v3 `database` catalog, binding generation, and guarded consumers | Tests for any missing resolver invariant | Replacing integrated catalog with the standalone `dbbinding` prototype |
| Evidence and logs | v3 `archive`, `retention`, and `logcollect` paths | Explicit event-cursor expiry and resync behavior | Replacing immutable archive/outbox with the narrower archive prototype |
| Control backend | Narrow consumer interfaces selected before connection | Etcd adapters and `storetest` suites | Connecting or migrating PostgreSQL for an etcd profile |

## Review units and merge order

1. **M1 interface taxonomy and PG behavior.** Preserve signed acceptance and generation-fenced execution. Port auth aggregate around the v3 session claim. Add shared invariant tests that both adapters must pass. Inventory all direct SQL and concrete `*store.DB` callers; migrate each control boundary without changing the Mini PG behavior.
2. **M2 archive and profile contract.** Make event replay expiry a real hub/client result. Reserve evidence transactionally for HTTP, cron, webhook, and child producers. Archive remaining domains with dependency-aware holds, read-through, verified restore, and bounded hot-state growth. Complete MySQL and real Nomad/object-service qualification.
3. **M3 etcd adapter.** Implement signed acceptance, monotonic claim generation, authority epoch, app lock, and effect persistence in etcd. Run the same invariant suite against PG and etcd. Add indexed, bounded reads rather than prefix-scan growth.
4. **M3 consumers and bootstrap.** Replace concrete PG dependencies in API, handler, pipeline, hub, beacon, worker, host agent, saga, readiness, and recovery tools. Select backend before any PG connection. Add TLS, member supervision, snapshots, offline restore, compaction, alarms, and three-member Fleet bootstrap.
5. **Qualification.** Rehearse concurrency and restarts for M1; Mini/Fleet binding, MySQL, archive outage and bounded growth for M2; PG-free startup, member/quorum loss, stale workers, full restore, corruption, quota and disk alarms for M3. Record exact commands, fixtures, versions, and measured results in milestone evidence before changing a gate to complete.

## Consumer inventory to close before PG-free startup

The durable branch's `v2/api/main.go` connects/migrates PG before selecting the backend. It then passes PG directly to operation recovery, hub, beacon, notification, saga, pipeline, and handler. Its host agent independently connects/migrates PG. The v3 branch also has concrete `*store.DB` fields in `handler`, `pipeline`, `beacon`, and `worker/maintenance.go`; those need narrow interfaces while preserving the PG adapter. SQL-specific readiness and `pgx` error handling must use backend-neutral status, capabilities, and errors.

## Gate interpretation

Passing package tests is a code checkpoint. M1 requires concurrent two-replica acceptance/ownership/effect proof and old-data compatibility. M2 requires unchanged Mini binding, independent Fleet binding, archive/index recovery with original signed bytes, bounded history/logs, and real PG/MySQL/Nomad/object-service paths. M3 requires the API and host agent to operate with no usable control PostgreSQL, then a fresh three-member Fleet bootstrap and failure/restore qualification. M4 design may use frozen M1/M2 contracts; loaded placement and 2→3→2 qualification depends on those gates and a qualified Fleet control plane.
