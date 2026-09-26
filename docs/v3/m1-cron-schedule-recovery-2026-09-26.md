# M1 cron schedule worker recovery — 2026-09-26

The `app.cron-schedule` path already accepts a signed operation, reserves the
exact Nomad schedule effect, and can observe its effect marker after a lost
Nomad response. Expired-operation recovery omitted this kind from the safe
requeue list, however. A worker death after reservation left the operation
failed before its existing effect reconciler could run. The recovery list now
requeues `app.cron-schedule` under a new operation claim.

Two opt-in integration tests drive HTTP admission, an isolated PostgreSQL 16
schema, and a disposable Nomad 2.0.7 periodic job. They abandon the first
worker's claim at the durable reservation boundary and expire its lease:

- Before a Nomad write, the successor leaves the reservation unresolved and
  does not change the periodic job.
- After one guarded Nomad schedule update but before effect completion, the
  successor observes the exact effect marker, records the terminal receipt and
  paused cron state, and returns that same receipt for the original HTTP key.
  The Nomad parent version advances exactly once.

The full `TestCronSchedule` handler group passed against these disposable
services, including normal execution, lost response, and two-worker
same-key tests. The container, agent, job, and scratch directory were removed.
The first two crash-window tests place durable state at the two boundaries; they
do not kill a separate OS process. A third opt-in test now starts the normal
operation worker in a separate process. Its loopback proxy forwards the one
guarded schedule registration to live Nomad, withholds the successful response,
and the parent sends `SIGKILL` while PostgreSQL still records the effect as
reserved. After expiring that disposable worker lease, a fresh worker process
reclaims the operation and verifies the exact Nomad effect marker. The effect
and operation become terminally successful, same-key HTTP replay returns the
original receipt, the Nomad parent version advances once, and the proxy
rejects any second schedule registration. This literal post-commit crash test
passed against disposable Nomad 2.0.7 and PostgreSQL 16.

A literal pre-write process kill, two replacement OS processes racing recovery,
and Mini runtime qualification remain open M1 gates.
