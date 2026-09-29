# M1 cron resume disposable qualification — 2026-09-25

`TestCronResumeHTTPWorkerNomadPostgres` passed twice against a disposable
Nomad 2.0.7 dev agent and PostgreSQL 17.7. The second run used the current
integration worktree and completed in 0.33 seconds. The test creates a unique
periodic job and isolated control schema, then exercises the HTTP handler with
a stable idempotency key. It observes signed `202` acceptance, execution by a
claimed operation worker, the exact Nomad resume effect marker, unpaused
PostgreSQL cron state, and a terminal operation receipt with an effect ID.
Replaying the same HTTP request after Nomad has changed returns the same
operation receipt, with one accepted operation row.

The test is opt-in because it requires disposable live services:

```sh
NORN_TEST_NOMAD_ADDR=http://127.0.0.1:14684 \
NORN_TEST_DATABASE_URL='postgres://postgres:<test-password>@127.0.0.1:15484/norn_test?sslmode=disable' \
go test ./handler -run '^TestCronResumeHTTPWorkerNomadPostgres$' -count=1 -v
```

The test job and database schema were removed, and the disposable agent and
database container were stopped. This proves the normal integrated path and
post-success replay.

`TestCronResumeLostNomadResponseReconciles` passed twice against the same
disposable versions. Its proxy forwards the guarded registration to real
Nomad, drains Nomad's successful response, then closes the worker-facing
connection. The worker receives an ambiguous transport failure and resolves
it from the exact effect marker. The test verifies one forwarded registration,
one Nomad parent-version increment, a completed successful PostgreSQL effect
record, a terminal operation receipt, and same-key HTTP replay with one
accepted operation. It can run alongside the normal-path test with
`-run '^TestCronResume(HTTPWorkerNomadPostgres|LostNomadResponseReconciles)$'`.

Two further deterministic boundary tests passed twice against the same live
disposable services. They abandon an old operation claim after durable effect
reservation, expire the claim in PostgreSQL, and let a successor claim recover.
Before any Nomad write, recovery leaves the effect unresolved and makes no
remote mutation. After one guarded Nomad write but before effect completion,
the successor observes the exact marker, completes the receipt, and returns
the same result on HTTP replay. The old claim cannot finish the operation.

These tests place durable state at the crash boundaries; they do not kill an
OS process during a remote write. A literal process-kill rehearsal and two
API/worker replicas racing remain M1 release gates.

## Distinct worker claim owners — 2026-09-25

Operation and maintenance workers now include a per-instance UUID in their
claim owner IDs. Hostname and PID alone gave two workers in one process the
same owner, weakening the two-worker qualification. With separate PostgreSQL
connections and distinct owner IDs, `TestCronResumeTwoWorkersSameKey` passed
against disposable Nomad 2.0.7 and PostgreSQL 16. The test observed one
accepted operation and one guarded Nomad parent-version increment. The full
five-test cron resume group also passed on those services. This is a
same-process, two-pool race; separate OS processes and process-kill recovery
remain open.
