# M1 cron trigger worker process-crash qualification

`TestCronTriggerWorkerProcessCrashNomadPostgres` is an opt-in literal
process-crash gate for the signed `app.cron-trigger` path. It uses a unique
periodic job on a disposable loopback Nomad agent and an isolated schema in a
disposable PostgreSQL 17 database.

The test accepts the trigger through the HTTP handler, then starts the normal
`worker.OperationWorker` in a separate Go test process. A transport barrier
allows its initial parent read through to Nomad, then holds its second parent
read. At that point the PostgreSQL `operation_effects` row is committed with
`lifecycle=reserved`, the worker is poised between reservation and Nomad
`PeriodicForce`, and the parent kills that worker with `SIGKILL`.

The parent expires the killed worker's disposable ownership lease and starts a
new normal operation-worker process against the direct Nomad address. Recovery
must retain the unresolved reservation, issue no `PeriodicForce`, and exhaust
the cron recovery budget into a terminal receipt with
`manualRecoveryRequired`, `externalEffectRecoveryPending`, and
`retryBudgetExhausted`. The test also proves that the periodic parent has no
children before or after restart.

Run it only against disposable services:

```sh
NORN_TEST_NOMAD_ADDR=http://127.0.0.1:14684 \
NORN_TEST_DATABASE_URL='postgres://postgres:<test-password>@127.0.0.1:15484/norn_test?sslmode=disable' \
go test ./handler -run '^TestCronTriggerWorkerProcessCrashNomadPostgres$' -count=1 -v
```

The test deregisters its unique parent and any child jobs and drops its schema.
It is a before-launch crash proof only. It does not qualify a kill after Nomad
accepts `PeriodicForce` or after a returned evaluation is awaiting durable
acknowledgement; those are separate M1 crash boundaries.
