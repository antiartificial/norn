# M1 cloudflared two-process qualification

The opt-in tests `TestCloudflaredTwoAPIProcessRacePostgres` and
`TestCloudflaredWrongHostWorkerProcessPostgres` require
`NORN_TEST_DATABASE_URL` pointed at disposable PostgreSQL. The first test
creates an isolated schema, local InfraSpec and cloudflared config, and a
PATH-scoped fake `launchctl`. It starts two distinct OS processes. Each
constructs a PostgreSQL-backed API handler, an HTTP teardown route, and a
claimed operation worker. Concurrent HTTP requests with the same actor and
idempotency key resolve to one signed operation. Its terminal state, one
effect row, the resulting ingress file, and one fake restart call are checked.

The second test starts a separate OS process with a host-B driver against an
accepted host-A operation. That process claims the work and calls the normal
pipeline executor. It must defer before an effect reservation, config write,
or restart. The first test also submits an accepted intent for a different
host while its two worker loops remain live. One child claims and defers it to
queued status with a wrong-host recovery reason, zero effect rows, and no
second restart. These cover both the direct claimed executor and the full
worker loop. The in-process crash tests separately cover the post-write and
post-restart ambiguous boundaries.

The disposable PostgreSQL 17 run passed on 2026-09-25. No live Mini config or
service was touched. Remaining release proof: actual Mini `launchctl` behavior
on a private copy, eventual correct-host reclaim of a deferred operation,
host receipt recovery after host replacement, and multi-host Fleet
ingress design. This qualification is for the PostgreSQL Mini host-local path.
