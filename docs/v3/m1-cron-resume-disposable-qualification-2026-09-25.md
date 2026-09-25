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
post-success replay. It does not cover a lost Nomad response, process death
between effect reservation and remote write, claim expiry during that write,
or two API/worker replicas racing. Those remain M1 release gates.
