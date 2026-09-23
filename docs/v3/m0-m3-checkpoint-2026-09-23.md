# M0–M3 integration checkpoint — 2026-09-23

This supersedes the implementation-status portions of the 2026-09-22 pause
handoff for these milestones. It records the reviewed code on
`feature/norn-v3-planning-handoff` through `7cabdc9`; it is not a release
qualification or authorization for a Mini/Fleet runtime change. No full M0–M3
exit gate is complete.

| Milestone | Integrated checkpoint | Remaining exit work |
| --- | --- | --- |
| M0 | [Read-only Mini baseline](m0-mini-baseline-2026-09-23.md): API release SHA, app/process/endpoint counts, active operations and incidents. | Pin installed binary/source/schema together; measure database bytes, growth and connections; map jobs/routes/volumes/DB ownership; make sanitized and private restore fixtures; reconcile ADR dispositions, owners and budgets. |
| M1 | Signed PostgreSQL acceptance, claim fencing, auth revocation aggregate and conformance slices. [Control-boundary audit](m1-control-boundary-audit.md) identifies concrete PostgreSQL consumers and eight inline external-effect paths. | Convert effectful producers to signed accepted work with durable effect reservation and ambiguous-result reconciliation; remove concrete-store bypasses; prove two-replica races, crash boundaries and old-data compatibility. |
| M2 | Named PostgreSQL bindings and guarded consumers; saga/effect and terminal Fleet GitHub receipt bundles; reserve, read-only archive recovery, replay-expiry resync and bounded diagnostic collection. Operation receipt archive/index recovery and immutable-conflict checks have focused PostgreSQL tests. | Reserve the Fleet GitHub action before external dispatch, bound hot receipt/identity lifetime without losing replay, add byte-based reserve accounting and remaining non-saga archive domains; qualify MySQL, real Nomad, object service, growth and restore under outage. |
| M3 | Etcd operation/auth adapters use canonical signed acceptance evidence, generation-fenced claims and atomic revocation/session admission. Shared tests and local live-etcd race tests cover acceptance tamper, large integers, concurrent caps and revocation. | Etcd mode still fails closed before PostgreSQL connection. Implement recovery, lease-backed app locks, aggregate admissions, effect persistence, bounded indexes and backend-neutral consumers; then TLS three-member Fleet bootstrap, PG-free API/host-agent operation, quorum/fault/restore/soak qualification. |

## Verification and limits

The combined branch passed `go test -race ./... -skip
'^TestSampleDarwinHostMetrics$' -count=1 -p 1` from `v2/api` with a
disposable etcd v3.5.16 member selected by `NORN_TEST_ETCD_ENDPOINTS`.
Changed archive, retention, store, recovery and etcd packages passed a second
race run after the final receipt-kind binding fix. Repository CI for the M2
and M3 PRs passed API, CLI, web and workflow checks. The disposable etcd
member was stopped. The exact Darwin host-metrics sampler exclusion is a
known test-environment issue; these tests do not prove live Fleet operation.

The next review units should first close the [M1 external-effect
bypasses](m1-control-boundary-audit.md), then the M2 pre-dispatch receipt and
hot-state bounds, then M3 backend-neutral consumer/recovery work. M0
measurement and fixtures can proceed in parallel. M5's isolated Mini upgrade
rehearsal can begin after the Mini PG M1/M2 contracts are qualified; it does
not require a live Fleet. M4 loaded capacity qualification requires the M1/M2
contracts and the qualified Fleet control plane.
