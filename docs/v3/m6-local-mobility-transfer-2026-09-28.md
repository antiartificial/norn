# Local mobility fixture transfer — 2026-09-28

The repeatable [`test-mobility-local-transfer.sh`](../../v2/scripts/test-mobility-local-transfer.sh)
passed against a disposable socket-only PostgreSQL cluster and the checked-in
mobility fixture. The rehearsal built the fixture binary locally, created
separate `source` and `target` databases and file directories, and removed
its scratch data and cluster after completion. The script SHA-256 for this
run was `37ca0247cb077b5f5e9c0a20c89ea257f416cda09d7c1a88efcd6e127b6e42ba`.

It created a source item, acknowledged its job and recorded a tick. A forced
read-only `pg_dump` populated the passive target. It then created and
acknowledged a second source item, stopped the writable source process,
restarted it with writes disabled, and observed HTTP 423 plus worker and
schedule write refusal. A second forced read-only dump replaced the target
database, and the source files were copied to the target file directory.
Both `/state` responses had writes disabled before comparison.

The source fixture used a dedicated `source_runtime` login. A separate
`source_fence` login held `CREATEROLE`, admin authority over only the runtime
role, `pg_signal_backend`, and `pg_read_all_stats`. With a runtime session
active, that delegated principal disabled the runtime login and terminated
its session. Readback showed `rolcanlogin=false` and zero runtime sessions;
the active session failed and a fresh login was rejected. The dump used a
separate maintenance principal.

`compare_state.py` reported **2 matching items, 2 acknowledged jobs, and 1
tick**, with matching IDs/digests and no missing, mismatched or orphaned files.
The fixture Go package and three comparison tests passed. This proves a local
full-replacement data/file transfer path and deterministic reconciliation for
the synthetic fixture. The source and target used separate databases on one
disposable server with trust authentication. Its delegated role was created
locally and was not resolved from the Norn catalog; provider support remains
unqualified. The rehearsal did **not** use separate providers, incremental
synchronization, Nomad, the Norn
cutover journal, generation-bound consumer credentials,
ingress/traffic switching, crash recovery, or rollback after target writes.
It does not sign M6 or M7.
