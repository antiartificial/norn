# Etcd Fleet runner-attempt aggregate qualification

Date: 2026-09-25

## Implemented boundary

`V3OperationStore` now accepts `fleet.runner-attempt` only as one etcd
compare-and-swap aggregate. The transaction verifies the completed immutable
capacity plan and protected dispatch binding, which contains the plan digest,
approved commit, dispatch nonce digest, run ID, and workflow URL. Raw dispatch
nonces are not accepted or persisted by this boundary.

The same transaction persists the signed operation receipt, immutable attempt
lineage, operation index, and a per-plan state revision. A retry resolves the
original signed receipt and rechecks the durable runner lineage. Heartbeat,
phase, and cancel transitions compare both the runner revision and the plan
state revision.

## Disposable evidence

The focused tests ran against a disposable local etcd v3.5.17 container:

```text
NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:<random-port> \
  go test ./etcdstore -run 'TestV3FleetRunnerAttempt' -count=1 -v
```

All three tests passed:

1. signed acceptance replay preserves the original attempt and stale
   revision-CAS updates fail;
2. two independent `V3OperationStore` instances race recovery and exactly one
   successor is accepted;
3. a changed nonce digest is rejected before any runner attempt is persisted.

The existing signed-execution and canonical-acceptance etcd conformance cases
also passed against that fixture.

`NORN_OPERATION_REPLAY_TTL` defaults to zero, so the normal etcd runtime can
use this aggregate with its default configuration. An explicitly nonzero replay
TTL currently fails runner-attempt admission before any write. Expiring that
identity safely requires proof that the runner and its external effects reached
a terminal reconciled state; support for that policy is a separate hardening
gate and must not be enabled for Fleet runner attempts yet.

## Release limitation

This slice intentionally matches the current PostgreSQL recovery contract: a
protected successor can cancel a live predecessor in the control record, and
the predecessor receipt states that external execution termination is
unproven. The aggregate prevents two writers from accepting the same successor
lineage, but it does not prove that a remote Fleet workflow stopped before the
successor begins.

Do not expose runner-attempt mutation routes or qualify automated recovery as
safe for provider changes until the Fleet workflow supplies a signed,
independently verified external cancellation or reconciliation proof. M3 still
requires that proof, end-to-end runner wiring, PG-free Fleet runtime coverage,
and the host-supervised three-member bootstrap, restore, and fault exercises.

## Current-head route audit — 2026-09-26

At Norn PR #76 head `c3d437d`, the normal etcd Fleet router registers the
attempt create, heartbeat, advance, and cancel routes in
`v2/api/etcd_fleet_runtime.go`. Its capability response also advertises
`fleet-runner-attempts-v1`. The adapter's `TestV3FleetRunnerAttemptAcceptsOneConcurrentRecoveryEtcd`
asserts that one of two successors is accepted while the predecessor remains
queued; the accepted transaction changes that predecessor's control record to
`canceled` and states that external execution termination is unproven.

This is a **merge/release blocker** for provider-changing recovery, not merely
a missing fault test. Before those routes and capability can be qualified for
v3, bind recovery admission to independently verified termination or
reconciliation of the exact predecessor workflow, then prove the evidence and
one-successor rule through the normal API with real protected runner identity.
Until that proof exists, a green local suite or PR check must not authorize
protected Fleet adoption or automatic successor dispatch.
