# Workload release admission and recovery model

This bounded TLA+ model describes the proposed M5 release-owned staging path.
It does not claim that admission or target staging is implemented. The current
worker still accepts an already pinned `WorkloadReleaseIntent`.

## Regenerate the chart

From the repository root, run:

```sh
~/.agents/skills/statechart/scripts/statechart \
  docs/v3/statecharts/workload-release-admission/Release.tla \
  --config docs/v3/statecharts/workload-release-admission/Release.cfg \
  --view docs/v3/statecharts/workload-release-admission/view.ts \
  --out /tmp/norn-m5-workload-release-statechart
```

Use a new or empty output folder for each run.

## Checked behavior

The model allows a successor claim to bind an exact matching job after a lost
response. It holds when the attempt marker exists but the job is absent, or
when the job identity does not match. A delayed create may still arrive after
the controller holds, but the old workload remains active and no route change
or stop follows from that late registration.

In code, the model's create permission and matching-target observation map to
durable runtime boundaries. The workload-release effect ledger validates a
stage reservation against the signed plan and shared app gate;
`MarkWorkloadReleaseStageTargetAttempt` grants one create attempt under the
live claim. `BindClaimedWorkloadReleaseTarget` then reloads the signed plan,
requires that attempt marker, derives the create-only Nomad lookup from its
target identity and job digest, calls `LookupDeploymentJobRevision`, and only
then writes the claim-fenced binding. The binding, successful stage-effect
completion, and app-effect-gate release share one etcd transaction.
The etcd integration test verifies that a not-found response writes nothing,
that the lookup uses the signed target with expected modify index zero, and
that a conflicting later revision cannot overwrite the first binding. The
model abstracts this observer as a trusted exact readback; it does not model
the Nomad HTTP request or a compromised observer implementation.

The route sequence keeps the old workload running through old-only, mixed, and
target-only routing. The old job stops only after the controller refreshes
full target health and target-only route proof. The invariants check that the
stop has that proof, the target has an exact bound identity, traffic does not
move before binding, and the old workload remains active until stop.

The model also pins an immutable encrypted secret revision in the plan. A
current-secret pointer may rotate independently without changing the accepted
release input. The controller must resolve the exact pinned revision before
recording the create attempt; if its encrypted snapshot is unavailable, it
holds before scheduler submission. The target binding records that same
revision. The model assumes secret plaintext remains outside operation and
plan data; it checks identity and availability, not cryptographic encryption.

The etcd acceptance seam now seals the exact encrypted SOPS snapshot with a
separate workload-release key ring and binds its AAD to authority, operation,
app, and canonical plan digest. Signed acceptance, the encrypted record,
one-active-app admission, and the current Fleet target revision commit
atomically. Replay opens the stored envelope and compares the submitted
snapshot before returning the original operation. A key inventory reports all
retained IDs. Startup preflight and deletion/retention policy are not yet wired
to this store.

The model also treats the workload envelope key as a recoverability input.
If a successor cannot load the historical key before exact target binding, it
holds the release and leaves the old route and workload untouched. Once an
exact target binding exists, this model no longer explores loss of the private
input key because that snapshot may be deleted under the retention contract.
The model allows a delayed create to arrive after a hold, but keeps the
operation held and the old service active.

The initial 2026-11-08 baseline checked 86 distinct states and 203 transitions. The chart
shows 16 states and 31 arrows; 109 model transitions remain hidden because
they stay within the same reader-facing state. All listed
safety invariants and deadlock checks passed. The first expanded run exposed a
model transition that let a delayed scheduler acceptance resume a held
operation; `NomadAccepts` is now limited to the active submitting phase, while
late acceptance after hold remains an external effect with no route or stop
progress. Target health loss is modeled only before completion so unrelated
post-release service health does not reopen a terminal release. The model
omits etcd transaction details, actual key retrieval and
cryptography, real Nomad and ingress behavior, workload data compatibility, and
provider failures. It is a protocol model, not a live qualification.

The expanded 2026-11-08 run checks 348 distinct states and 1,228 transitions.
It adds abstract signed-plan acceptance, request-key replay of the original
plan, app mutation fencing, and route reservation. Five added invariants check
that acceptance carries both fences, replay retains the original acceptance,
a competing app writer cannot enter while the fence is held, and the writer
attempt is accounted for. All 15 listed invariants and deadlock checking pass.
The chart draws 18 reader-facing states and 49 arrows; 864 transitions
stay within a displayed state and are hidden. The model treats acceptance and
fencing as one atomic abstract step. It does not model etcd compare revisions,
conflicting request fingerprints, route-writer internals, or exercise real
handlers against the modeled steps. Those remain runtime integration and code
review obligations.

The generated [statechart](chart.mmd) and [TLC report](report.md) show the
checked transition graph and result. The conservative hold after an
attempt-marker crash is deliberate. A future retry protocol needs separate
Nomad-version, ACL, retention, and delayed-request qualification.
