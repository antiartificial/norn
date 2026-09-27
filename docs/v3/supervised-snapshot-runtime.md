# Supervised app snapshots

`app.snapshot` requires the durable external-effect runner. With
`NORN_SNAPSHOT_EXECUTION` unset, the API rejects new snapshot requests before
signed acceptance with HTTP 503 `snapshot_execution_unavailable`. Already
accepted work remains in the operation store for operator reconciliation; it
does not fall back to an inline `pg_dump`.

To enable the runner on a Linux host with delegated, writable cgroup v2,
configure:

- `NORN_SNAPSHOT_EXECUTION=supervised`
- `NORN_EFFECT_SUPERVISOR_ROOT`, `NORN_EFFECT_CGROUP_ROOT`, and
  `NORN_EFFECT_SIGNING_KEY` (at least 32 bytes)
- an absolute `NORN_EFFECT_RUNNER_BINARY` and its
  `NORN_EFFECT_RUNNER_SHA256` release pin
- an absolute, regular `NORN_SNAPSHOT_PGDUMP_PATH` and its
  `NORN_SNAPSHOT_PGDUMP_SHA256` pin
- `NORN_SNAPSHOT_ARTIFACT_BUDGET_BYTES` of at least 68,719,476,736 bytes
  (one 64 GiB maximum dump); reserve more for concurrent or unreconciled
  attempts
- `NORN_SNAPSHOT_TIMEOUT` within the runner's supported maximum

An incomplete supervised configuration fails startup. The manager reserves a
maximum-size private artifact allowance before launch. A successful operation
publishes an attested dump and sidecar under the configured snapshot root;
private dump removal follows the durable terminal operation write. Startup
reconciles cleanup after a crash in that interval. Unknown or unresolved
effects retain their admission allowance until trusted recovery resolves them.

Publication first commits one immutable, operation-bound intent in control PG
(schema migration 16, writer version 13). A verified public dump and sidecar
then receive a durable publication receipt. Claim successors can finish the
same intent after a lost database session or process crash; they cannot choose
different artifact bytes or a different target. A prepared or published intent
prevents terminal failure until its public state is reconciled. The runner's
signed terminal result is retained for exact effect replay after a host reboot.

The [Linux cgroup harness](snapshot-cgroup-linux-integration.md) proves the
runner and credential cleanup in a disposable container. The PostgreSQL
pipeline and store tests cover reservation, reboot replay, publication intent,
claim turnover, and receipt recovery with disposable PostgreSQL. These are
local qualifications; Mini restore, Fleet storage
budgets, two-replica behavior, and a deployed Linux runner remain release
gates.
