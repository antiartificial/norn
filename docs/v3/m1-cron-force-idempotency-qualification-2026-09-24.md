# M1 cron force idempotency qualification — 2026-09-24

This is a local compatibility test, not a release qualification. It was run
against a disposable Nomad 2.0.7 dev server bound to `127.0.0.1:14646` with a
periodic `raw_exec` batch job named `norn-cron-force-qual-0924`. The job's
normal schedule was midnight; no production Norn or Fleet resource was used.

The client package's `Jobs().PeriodicForce(jobID, *WriteOptions)` accepts a
generic `IdempotencyToken` and sends it as the `idempotency_token` query
parameter. That API shape suggested it might be possible to retry a forced
run safely after a worker crash or an ambiguous HTTP response. The actual
server behavior disproved that assumption:

| Request | Token | HTTP status | Evaluation ID |
| --- | --- | --- | --- |
| First force | `norn-cron-force-qual-0924-token` | 200 | `ca9888f7-3147-7911-b26e-8139345a91a8` |
| Immediate repeat | same | 200 | `3b725958-5eb0-29da-f3be-ef8b0bc32b8f` |
| Later repeat | same | 200 | `ff624041-f26d-5c7d-8f6e-a6332a48994a` |
| Final repeat | same | 200 | `0018704c-9a7c-6ce4-e1d0-1fc97a687621` |

After the four requests, the Nomad jobs listing contained **three periodic
children**. The immediate pair landed in one second and shared a child ID;
the later requests created additional children. Different evaluation IDs
alone would not establish distinct execution, but the additional child jobs
do. The token therefore cannot be used as a durable effect identity for
`CronTrigger` on the tested Nomad version.

At the time of this measurement, `handler.CronTrigger` directly called `PeriodicForce` and returned its
evaluation ID. A signed operation alone would not repair this path: a retry
after Nomad accepts the force but before the worker receives its response
could force a second run. The smallest safe conversion needs a deterministic
child/evaluation identity accepted by Nomad or an independently proven way to
find the exact committed run before any retry. If neither is available, the
worker must stop at an unresolved effect for operator review rather than
force again. That still requires a complete reservation, claim, and recovery
path; queueing an operation that later fails execution is not sufficient.

`CronResume` and `CronUpdateSchedule` use `SubmitJob` rather than force. Their
candidate conversion must bind the exact job specification and effective
schedule, use a guarded Nomad registration with a durable effect marker,
reconcile a lost response from the periodic parent, and commit cron state
with the operation's live claim. The accepted payload must not persist
plaintext secrets. These are separate work items from the force behavior
observed here.

## Implementation checkpoint — 2026-09-25

`CronTrigger` now queues a signed operation. A claimed worker reserves the
one-shot external effect before calling `PeriodicForce`; an ambiguous response
never causes a second Force call. A bounded retry ends in a failed receipt
with a manual-recovery hold. The normal HTTP-to-worker route and its Nomad
evaluation lineage passed in disposable Nomad 2.0.7/PostgreSQL 17.7.

A separate signed `app.cron-trigger-reconcile` operation accepts a positive
evaluation ID supplied by an operator. Admission and execution verify its
periodic parent and Nomad trigger type. One fenced PostgreSQL transaction
completes the reserved effect, correction receipt, and archive intent without
rewriting the original failed receipt. The evidence hold remains until the
correction archive is verified. PostgreSQL tests cover mismatched app/effect,
forged evaluation, stale claim, duplicate evaluation credit, replay, and the
hold transition; the full store suite passed.

The opt-in live rehearsal then passed against disposable Nomad 2.0.7 and
PostgreSQL 17.7. A proxy delivered exactly one Force and dropped its response.
After bounded source retries, the operator submitted the real evaluation ID
through HTTP; the worker completed the correction while the failed source
receipt remained unchanged. The local archiver published and read back both
bundles. The recovery hold stayed until the correction archive was verified,
then cleared. The child/parent jobs, Nomad agent, and database fixture were
removed. This is local evidence, not a Mini or Fleet rollout.

If an ambiguous Force has no provable evaluation ID, Nomad provides no safe
absence proof on this version. That reservation remains held for operator
investigation; automatic retry or release would risk a duplicate run.

## Schedule update conversion — 2026-09-25

`CronUpdateSchedule` now queues a signed `app.cron-schedule` operation. The
claimed worker binds the old and new schedule, parent version/index, spec,
image, and database delivery revision; it reserves a Nomad effect before a
CAS registration, then commits cron state, operation receipt, and archive
intent together. It reconstructs private material in the worker and preserves
the parent's paused state.

Opt-in HTTP-to-worker tests passed against disposable Nomad 2.0.7 and
PostgreSQL 17.7 for both an ordinary update and a dropped registration
response. The tests observed one parent version increment, one forwarded
registration after the lost response, same-key replay, persisted schedule,
and paused-state preservation. The unique job and schema were removed and
both services stopped. Literal process-kill and two-worker schedule races
remain part of the M1 qualification gate.
