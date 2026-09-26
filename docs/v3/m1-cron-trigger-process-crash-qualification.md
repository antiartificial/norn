# M1 cron trigger worker process-crash qualification

The opt-in literal process-crash gates for the signed `app.cron-trigger` path
use a unique periodic job on a disposable loopback Nomad agent and an isolated
schema in a disposable PostgreSQL 17 database.

`TestCronTriggerWorkerProcessCrashNomadPostgres` accepts the trigger through
the HTTP handler, then starts the normal
`worker.OperationWorker` in a separate Go test process. A transport barrier
allows its initial parent read through to Nomad, then holds its second parent
read. At that point the PostgreSQL `operation_effects` row is committed with
`lifecycle=reserved`, the worker is poised between reservation and Nomad
`PeriodicForce`, and the parent kills that worker with `SIGKILL`.

The parent expires the killed worker's disposable ownership lease and starts
two normal operation-worker processes against the direct Nomad address. Recovery
must retain the unresolved reservation, issue no `PeriodicForce`, and exhaust
the cron recovery budget into a terminal receipt with
`manualRecoveryRequired`, `externalEffectRecoveryPending`, and
`retryBudgetExhausted`. The test also proves that the periodic parent has no
children before or after restart.

`TestCronTriggerWorkerProcessCrashAfterNomadForceNomadPostgres` crosses the
next boundary. Its test-only proxy forwards the worker's `PeriodicForce` to
live Nomad, drains the successful response, records the exact `EvalID`, and
withholds that response. The parent proves the exact evaluation exists against
the real periodic parent while the durable effect remains `reserved` with no
runtime ID, then kills that separate worker with `SIGKILL`. A replacement
normal worker receives the same unresolved reservation through elapsed-lease
recovery. The proxy rejects and counts any later Force, and the test requires
exactly one Force call. Recovery exhausts into the same manual-review receipt
without writing an evaluation acknowledgement. This proves the actual Nomad
side effect is retained as ambiguous rather than replayed.

`TestCronTriggerWorkerProcessCrashAfterNomadAcknowledgementNomadPostgres`
crosses the final normal-worker acknowledgement boundary. Its proxy forwards
the successful Force response, then holds the exact evaluation read that the
worker can only make after `MarkLaunched` commits. The parent proves the effect
is durably `launched` with the exact evaluation ID, kills the worker, and
releases the evaluation read for a successor still routed through the same
proxy. That successor re-verifies the recorded evaluation and commits the
completed receipt without issuing Force a second time; the proxy rejects any
duplicate Force before it reaches Nomad.

Run it only against disposable services:

```sh
NORN_TEST_NOMAD_ADDR=http://127.0.0.1:14684 \
NORN_TEST_DATABASE_URL='postgres://postgres:<test-password>@127.0.0.1:15484/norn_test?sslmode=disable' \
go test ./handler -run '^TestCronTriggerWorkerProcessCrash(NomadPostgres|AfterNomadForceNomadPostgres|AfterNomadAcknowledgementNomadPostgres)$' -count=1 -v
```

Each test deregisters its unique parent and any child jobs and drops its
schema. Together they prove the pre-Force reservation boundary and the
post-Force, pre-acknowledgement, and post-acknowledgement boundaries. The
success path after acknowledgement is replayed from the durable evaluation
identity instead of repeating Nomad's non-idempotent Force.

## Current integration branch rerun — 2026-09-25

All three tests above passed on Norn PR #76 after worker claim owners gained a
per-instance UUID. The disposable endpoints were PostgreSQL 16 on loopback
and Nomad 2.0.7 in dev mode. This is literal `SIGKILL` and separate-process
recovery for cron trigger, including a two-process replacement race before
Force. It does not establish crash recovery for every other operation kind
or qualify the production Mini/Fleet runtime.
