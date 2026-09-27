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

The next Norn source change enforces that boundary in the etcd adapter: any
successor request now fails with
`fleet_runner_attempt_external_stop_unproven` before creating an acceptance or
rewriting the predecessor, including when its heartbeat expired or its control
status is terminal. First-attempt admission, same-identity replay, heartbeat,
and signed phase advancement remain available. A disposable etcd v3.5.17 run
passed the focused runner tests, including two concurrent rejected successors
and an unchanged queued predecessor. The ordinary PG-free Fleet API process
test also passed with both absent and poisoned PostgreSQL DSNs. This prevents
unproven automatic recovery; it does not implement the external-stop proof or
qualify protected provider-changing recovery, so the M3 gate remains open.

The GitHub App client now has a read-only `ObserveApplyRun` seam for the exact
signed-dispatch binding. It verifies the repository URL, workflow path, branch,
commit, app actor, plan inputs, and private dispatch nonce before returning
GitHub's run attempt, status, conclusion, and observation time. Its focused
fake-GitHub test rejects a different nonce and an unnumbered run; the full
`githubapp` package passes. A `completed` response is only a point-in-time
observation: a later rerun can increment `run_attempt`, and GitHub terminal
state alone does not reconcile provider effects. The next gate is a durable,
signed exact-run observation plus a rerun/fencing and provider-reconciliation
contract, checked atomically before enabling successor admission.

The normal runner HTTP integration fixture now exercises the fail-closed
successor route against disposable etcd v3.5.17. A protected first attempt,
same-key replay, signed reconciliation checkpoint, and phase advance pass. A
different authenticated recovery run then receives HTTP 409 with
`fleet_runner_attempt_external_stop_unproven`; the predecessor's status and
revision remain unchanged and the plan still has exactly one attempt. This
proves the API boundary currently rejects unproven recovery. It does not prove
that a future successful successor can safely apply provider changes.

## Successor proof contract — 2026-09-26

The GitHub App client can now read an exact numbered workflow attempt using
GitHub's `/actions/runs/{run_id}/attempts/{attempt_number}` endpoint. It checks
the protected dispatch identity and then reads the current run to reject an
already visible rerun or disagreeing state. The verifier accepts GitHub's
default-branch `workflow.yml@branch` path and rejects another ref. Norn PR #76
head `48a164a` passed the `githubapp` package locally and all repository CI
jobs. This is still a point-in-time read; it is not an admission proof.

Before allowing a successor in the etcd aggregate, the protected recovery
lane must provide all of the following evidence, bound to the plan and exact
predecessor runner attempt:

1. A durable, authenticated observation of the predecessor's numbered GitHub
   workflow attempt and terminal outcome, with a checked current attempt
   number. A generic run URL, elapsed heartbeat, or control-record cancel is
   insufficient.
2. A fresh provider and backend reconciliation for the exact approved plan and
   root, identifying completed effects and the safe remainder. The proof must
   account for any child process or side effect that could outlive a terminal
   GitHub job.
3. A fencing rule for a late predecessor or rerun. A new GitHub run attempt
   must obtain its own Norn admission and cannot reuse the predecessor's signed
   receipt; provider-changing steps must stop when their authority is lost.
4. An atomic etcd compare-and-swap that verifies the proof's immutable identity,
   predecessor revision, plan state revision, and one-successor lineage while
   accepting the recovery attempt. A failed compare must leave both attempts
   and the proof unchanged.

The current implementation supplies none of that combined durable proof to
the adapter. It must continue returning
`fleet_runner_attempt_external_stop_unproven` for every successor. The Fleet
GitHub `contract` check is separately blocked before runner assignment by the
account billing/spending-limit condition; local Fleet tests cannot close that
hosted CI gate.
