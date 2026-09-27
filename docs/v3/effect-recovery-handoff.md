# M1 external-effect recovery implementation handoff

Status: first supported effect (`build.test` in deploy/preflight) implemented
locally behind `NORN_BUILD_TEST_EXECUTION=supervised`; see implementation
status. Real Linux cgroup containment, Docker/BuildKit and audited
reconciliation remain unimplemented or unqualified.

## Implemented contract (local, unqualified)

- One unresolved effect per resource (`app/<app>/build.test`). A blocked
  successor recovers the exact holding execution from trusted evidence and
  never launches until that evidence releases the gate.
- The input digest binds command, environment, timeout and the pinned clean
  commit, not the per-claim checkout; later claims of the same operation reuse
  a completed result or query the original execution.
- A contained self-exit or runner timeout is a final failure (`failed`), not a
  repeat-safety claim. A stopped/revoked or unknown execution stays gated.
- Runner output is bounded, fsynced and bound by length and digest into the
  signed terminal status; retrieval rejects anything that does not match.
- The supervisor registry records the runtime before start; lost or rolled-back
  history fails closed instead of reading as never launched.
- Pending effects defer the operation without failing or publishing the
  deployment, step or saga.
- Legacy mode is explicit and unchanged; supervised mode has no fallback.
- Per-operation execution checkpoints (migration 4) fix the source identity
  and build image on the first claim; later claims reuse the build and must
  reproduce the same source tree, or fail without running another test.
- Timeout kills are joined before the command leader is reaped and, on Linux,
  target a dedicated command cgroup through a directory descriptor.

## First implementation seam

Add durable effect ownership and a supervised execution boundary for build/test.
`pipeline/test.go` runs arbitrary shell commands; `pipeline/build.go` invokes
Docker/BuildKit and registry push. Neither shell cancellation nor Docker client
exit proves downstream effects stopped or that repeating them is safe. Replace
the stage-name crash-retry allowlist in `store/operations.go` with evidence-based
recovery, without permanently disabling supported work.

Proposed ownership:

- `store/operation_effects.go`: durable effect lifecycle and resource reservation.
- Additive migration: immutable effect identity and unresolved resource gate.
- `worker/operations.go`: gate execution across operation identities, not only retries.
- `pipeline/pipeline.go`: pass effect context with the current operation claim.
- `pipeline/effect_commands.go`: injectable supervisor boundary.
- Build/test execution: use the boundary rather than direct command launch.

An effect binds control authority, resource, operation ID, claim generation,
stage and input digest. Its executor identity contains a durable supervisor
execution ID and runtime instance identity; a PID alone is insufficient.

## Required state rules

1. Atomically verify the live operation claim and reserve the resource before
   launching any external work. Persist launch intent first.
2. Claim expiry, cancellation and operation failure do not release unresolved
   resource ownership. A new request may queue but cannot bypass that gate.
3. Persist completion or verified resolution before releasing ownership.
   Stale completion cannot release a successor's reservation.
4. Recovery queries the same supervisor execution identity instead of launching
   again after an ambiguous response.
5. Proven termination and repeat safety are distinct. Arbitrary test commands
   may already have committed remote writes. Require adapter evidence or an
   explicitly audited reconciliation decision before replay.
6. Unknown effects preserve the gate while unrelated resources continue.

Resolution must originate from a trusted supervisor/downstream adapter or
audited operator reconciliation, never an unverified caller `stopped=true`.
An independent supervisor needs durable launch/query/stop behavior. Linux
cgroup containment can establish descendant termination; bare process groups
cannot contain arbitrary daemonizing children. Docker/BuildKit requires its
own downstream identity and outcome adapter. Do not infer daemon completion
from client exit. Qualify macOS containment separately; do not claim Linux
process guarantees for the Mini.

## Migration and compatibility

Store immutable identity/input digest, ownership generation, supervisor
identity, lifecycle, evidence references and timestamps. Enforce at most one
unresolved reservation per resource. Raise the writer contract because older
workers bypass this gate, and integrate that change with the all-writer
handoff rather than silently permitting mixed writers. Existing in-flight
effects cannot be backfilled as safe: represent unresolved legacy execution
explicitly. Include these records in the control recovery registry; restored
leases and supervisor identities confer no execution authority.

## Deterministic acceptance

- Pause after reservation, expire the owner, and prove another worker cannot launch.
- Fail/cancel that operation and prove a newly accepted request cannot bypass it.
- Interrupt before launch, after launch, and after completion before acknowledgment.
- Restart the supervisor and recover the original execution without duplicate launch.
- Keep a child alive after parent exit; retain the gate until containment proves stop.
- Demonstrate Docker client exit is insufficient to resolve daemon-side work.
- Resolve with verified evidence; exactly one successor proceeds.
- Reject stale completion, and allow unrelated resources to proceed.
- Execute a successful safe retry after resolution, proving recovery is usable.

Later units reuse this contract for Nomad conditional registration, database
migration/restore outcomes and the terminal-operation/automatic-rollback
outbox. This seam alone cannot establish exactly-once external effects or full
M1 completion.
