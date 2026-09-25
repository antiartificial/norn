# Etcd Fleet reconciliation admission qualification

Date: 2026-09-25

## Implemented boundary

`V3OperationStore` now accepts `fleet.reconciliation` only through an etcd
compare-and-swap aggregate. The aggregate reads the immutable successful
capacity plan, plan-state fence, complete per-plan reconciliation history, and
the named runner attempt. For active runner evidence it requires a live lease,
matching runner identity and workflow URL, matching commit and plan digests,
and the attempt's current phase.

The successful transaction writes the signed operation receipt, operation
indexes, append-only per-plan reconciliation record, and the next plan-state
revision together. The plan-state compare fences concurrent runner-attempt
creation, recovery, heartbeat, phase, cancellation, and reconciliation writes.
The shared transition validator requires each successful phase's prerequisite
evidence, including drain evidence before completion when the plan requires a
drain. Every new etcd runner attempt begins at `prechange_verified`, making
that mandatory first checkpoint admissible for each plan shape.

An identity replay resolves its original signed receipt before evaluating live
attempt state. A replay therefore remains safe after a later phase advance,
lease expiry, or retry-lineage change; it cannot append a second checkpoint.

## Disposable evidence

The focused tests ran against a disposable local
`quay.io/coreos/etcd:v3.5.17` container:

```text
NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:<random-port> \
  go test ./etcdstore -run '^TestV3Fleet(Reconciliation.*|Runner.*)$' -count=1 -v
```

All eight focused tests passed. They prove signed reconciliation replay and
phase-sequenced evidence, reject a wrong attempt, wrong current phase, and
mismatched evidence binding before an operation is written, and race two
independent adapters so exactly one reconciliation admission wins. The prior
runner-attempt replay, revision-CAS, recovery race, and forged-dispatch tests
also passed in the same fixture. The expanded runner tests refuse an advance
without signed current-phase evidence and race a phase advance against evidence
acceptance across two adapters.

## Release limitation

This is an etcd storage contract, not end-to-end Fleet execution
qualification. An `advance` now checks a signed, successful current-phase
receipt bound to the same plan, attempt, commit, and plan digest, while its
plan-state compare-and-swap remains valid. The check is still separate from the
evidence append transaction: a concurrent append makes the advance CAS lose
and callers must retry using the new state.

Do not claim provider-safe automated phase progression from this slice. M3
still requires disabled-route review and full runner wiring, PG-free runtime
coverage, and the host-supervised three-member bootstrap, fault, restore, and
soak exercises.
