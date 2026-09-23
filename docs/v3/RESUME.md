# Norn v3 pause and resume handoff

Paused at the user's request on 2026-09-22 (America/Chicago).
The full M0–M9 goal is paused, not completed. No full milestone exit gate is
claimed achieved. Significant M1/M2 implementation checkpoints are verified.

## Workspace and execution state

- Worktree: `/Users/arti/Desktop/Claude/norn-v3-foundations`.
- Implementation originated on `codex/v3-foundations`; baseline: `7304f39`.
  The checkout is now on `feature/norn-v3-planning-handoff`, created to commit
  this documentation package only. Implementation changes remain in this same
  worktree and are not included in the documentation commit.
- Implementation changes are uncommitted, including many untracked source/test files.
  Preserve the entire worktree. Do not reset, clean, or broadly stage it.
- Only the documentation package is being committed for the pause handoff.
  No implementation commits, pushes, releases, deployments, or live workload
  migrations were performed in this workflow. Main and Fleet checkouts were
  outside implementation scope.
- Claude CLI was the sole implementation writer; Codex independently reviewed,
  added regressions, and reran tests. Latest implementation pass completed;
  its follow-up was launched and then interrupted for this pause before any
  new implementation checkpoint. The process check found no matching Claude
  CLI process after interruption. No implementation worker is intentionally
  left running.
- Claude resume session: `1cb95013-1361-40ef-b675-7a1866c93279`.
  CLI: `/Users/arti/.local/bin/claude`. Resume only after the user resumes work;
  first recheck processes and current files. Do not infer completion from the
  session's report alone.
- Existing disposable PostgreSQL fixtures were used. Their availability is
  temporary; revalidate before reuse. Do not delete whole databases or stop
  unrelated database services.

## Milestone disposition

| Milestone | Achieved checkpoints | Remaining exit requirements |
| --- | --- | --- |
| M0: decisions/baseline | Roadmap, six ADRs, execution plan, contracts, handoffs and source inventory exist | Live Mini baseline, measured budgets, fixture/rehearsal fidelity, decision/owner closure |
| M1: shared control | Durable signed acceptance/idempotency slices; execution leases/fencing; schema ledger and passive startup; control recovery CLI; supervised build/test recovery | Complete shared store boundaries and all producer/effect coverage; runtime containment/reconciliation; complete compatibility/rollback proof |
| M2: profiles/DBs/retention | Named PostgreSQL resolution and consumers; catalog/target guards; immutable saga archives; local and emulated S3 paths; archive recovery; initial reserve gate; bounded labelled diagnostic spool/history | Non-saga archival, atomic all-producer reserve accounting, remaining review findings, MySQL, complete profile/UI parity, real runtime and growth qualification |
| M3: etcd/fresh Fleet | Planning/contracts | Etcd backend and invariant suite; host bootstrap/TLS/membership; snapshots/restore/compaction; three-node Fleet without control PG; fault/soak gates |
| M4: capacity/placement | Planning/contracts | Durable scale intent, role/pool enrollment, spreading, capacity/drain/routing, interruption recovery; 2→3→2 under load |
| M5: Mini upgrade | Supporting schema/passive/recovery foundations | Isolated representative upgrade/rollback rehearsal preserving IDs, data, jobs/routes and receipts |
| M6: running upgrades/DB cutover | Fencing/checkpoint/target-identity foundations; accidental target switches refused | Rolling A→B proof, quorum-safe upstream upgrades, external migration journal, writer fence/catch-up/switch/reconnect/recovery; PG/MySQL qualification |
| M7: app mobility | Planning/contracts | Representative Mini→Fleet app/data/files/jobs/traffic migration with ownership and rollback proof |
| M8: release | Local tests only | Client parity, signed exact-version artifacts, runtime/fault/soak/growth evidence, runbooks and release acceptance |
| M9: adoption | Not executed | Separately approved Mini upgrade, empty DO Fleet launch, selected workload migration |

Initial release intent remains retained-PG Mini plus fresh three-control-node
etcd Fleet. General PG↔etcd conversion, autonomous cloud scaling, multi-region
HA and CockroachDB are not silently added to initial scope. Ordinary WordPress
requires the planned MySQL adapter.

## Verified implementation checkpoints

- PostgreSQL catalog/binding identities and generations; independent control/app
  resolution; private credentials; migrate/snapshot/restore/probe plumbing;
  web/worker/cron/function delivery rendering. TLS verify-full has scoped PG
  evidence; actual Nomad template/allocation delivery is not qualified.
- Catalog activation is durable and claim-fenced. Regression fixes reject
  expired claims, foreign revisions, renamed target replacements, and known
  conflicts concealed by ambiguous history. Baselines are operator attestations
  plus target probes, not observations of all legacy writers.
- Control export/inspection/recovery and supervised build/test effect recovery
  have local integration evidence, not complete M1 external-effect coverage.
- Saga evidence: terminal outbox/backfill, immutable publication/readback,
  signed-content binding, late supplementary events, shadow comparison,
  holds/pruning, archive-aware history and index rebuilding.
- Local archive confinement/quota locking and S3-compatible conditional writes;
  S3 tests use an in-process emulator, not DigitalOcean Spaces qualification.
- Reader floor and connected-session checks, hold rechecks after external
  verification; reproduced hold-during-verification regression fixed.
- Diagnostic command capture is bounded during execution and redacts tested
  truncation-boundary secrets. Nomad tasks receive explicit rotation settings.
- Collector labels node/allocation/task/stream, resumes with deduplication/gap
  reporting, uses a bounded local spool with loss counters/torn-tail recovery,
  and serves app-authorized historical queries. Real Nomad/rotation and Fleet
  cross-node retrieval are not qualified.
- Initial durable evidence reserve blocks tested HTTP mutations under backlog
  or archive pressure. This is not atomic all-producer byte reservation.

## Latest independent verification

Full API suite passed with race detection and both disposable PostgreSQL URLs:

`go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1`

Examples: pipeline 28.572s; retention 19.500s; startup 25.793s; store 14.625s;
worker 20.284s; recovery CLI 19.905s; handler 15.121s; logcollect 5.139s;
archive 4.510s. Only the known Darwin host-metrics sampler test was excluded.
Tests include real scoped PostgreSQL, filesystem fixtures, S3 emulators and
fake Nomad HTTP. They are not live infrastructure evidence. Later edits must
be reverified; green tests do not close the semantic gaps below.

## Next implementation batch (dispatched, then interrupted)

1. Read `retention-review-checklist.md` and `retention-implementation-handoff.md`.
   Preserve all reviewer regressions and existing migration checksums.
2. Split read-only archive opening from writer capability probing.
   `archive-verify`/reindex must support Get/List-only credentials with zero
   PUTs. Make checksum-only versus authenticated recovery explicit.
3. Enforce evidence reserve transactionally at authoritative acceptance, for
   HTTP and internal producers (cron/webhooks/children), including queued/running
   obligations and oversized evidence. Use DB time. Preserve idempotent replay
   of already accepted work under exhaustion. The two-second HTTP cache and
   pending-saga count do not establish a storage bound.
4. Implement dependency-safe non-saga evidence archive/prune/read-through:
   terminal operation metadata/output, signed acceptance, checkpoints/effects,
   then remaining historical domains. Preserve replay/recovery/current/rollback
   holds and original signed bytes. Control events require cursor expiry/resync.
5. Bound spool metadata/directory growth as allocations churn; test follower
   fairness, terminal error versus EOF, batch-function discovery and scoped
   retrieval. Address host/node disk budget and Fleet collector/retrieval design.
6. Recheck remaining reader-census/new-session and phantom-hold races, archive
   list bounds/deadlines, and recovery behavior. Then run focused and full tests,
   update status, and progress toward M3 without declaring M2 done prematurely.

## Resume order and authority

Read this file → execution-milestones.md → implementation-status.md → latest
retention review/handoff → relevant ADR and source/tests. Earlier implementation
status sections are historical; use the latest checkpoint and current source.

Before editing: verify branch/status, review any changes since this pause,
confirm no other writer is active, and revalidate available test runtimes.
Continue with Claude CLI if the user keeps that workflow; do not start competing
writers. No provider mutation, SSH, install/new runtime, commit/push/release or
deployment authority is implied by this handoff. Obtain separate scope for
live qualification/adoption. Never add agent coauthor trailers.
