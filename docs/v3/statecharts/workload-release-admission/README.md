# Workload release admission and recovery model

This bounded TLA+ model describes the M5 release-owned staging and recovery
protocol. The claimed worker now executes signed-plan target staging and
resolves the observed binding to the existing strict
`WorkloadReleaseIntent` downstream contract. The opt-in authenticated staging
API is implemented in PR #319, and PR #321 covers fresh admission and exact
replay through the HTTP handler with deterministic fakes. Production-driver
and restart qualification remain incomplete.

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

The separate admission replay model isolates the idempotency contract. Its
stable request comprises a qualification ID and a target replica count bounded
by an operator-approved app policy. The handler implements this request in
PR #319; PR #321 verifies fresh selection and exact replay ordering. The model
checks that an exact same-key retry
returns the originally accepted receipt without rereading mutable
qualification, source, or secret inputs, even after expiry or source loss.
Changed qualification or target size conflicts; an out-of-policy size is
rejected before acceptance:

```sh
~/.agents/skills/statechart/scripts/statechart \
  docs/v3/statecharts/workload-release-admission/AdmissionReplay.tla \
  --config docs/v3/statecharts/workload-release-admission/AdmissionReplay.cfg \
  --view docs/v3/statecharts/workload-release-admission/replay-view.ts \
  --out /tmp/norn-m5-admission-replay
```

The 27-state, 60-step check passes all six replay/admission invariants and
deadlock checking. Its generated chart and report are stored in
`qualification-replay/`.

## Check missing admission evidence

Admission requires a signed staging qualification, freshly observed Nomad
serving lineage anchored to a trusted durable job digest, a current active
route predecessor/reservation, and a durably retrievable encrypted input
snapshot before it reserves the app and route. Serving lineage and route
predecessor are separate evidence: route ownership does not prove the current
Nomad job, version, digest, or healthy allocation set. Each missing-evidence
config removes exactly one prerequisite and must end in rejection without
creating those reservations:

```sh
for scenario in Qualification Predecessor ServingLineage Snapshot; do
  lower=$(printf '%s' "$scenario" | tr '[:upper:]' '[:lower:]')
  ~/.agents/skills/statechart/scripts/statechart \
    docs/v3/statecharts/workload-release-admission/Release.tla \
    --config "docs/v3/statecharts/workload-release-admission/Release-Missing${scenario}.cfg" \
    --view docs/v3/statecharts/workload-release-admission/view.ts \
    --out "/tmp/norn-workload-release-admission-missing-${lower}"
done
```

The four bounded checks each reached four states and six steps. They all
passed `TypeOK`, `IncompleteAdmissionIsRejected`,
`AcceptedPlanHasCompleteEvidence`, and deadlock checking. Their two-state
charts and TLC reports are under `admission-gates/`. These checks model the
admission decisions abstractly; they do not individually exercise each
missing-evidence rejection through the HTTP handler. The handler test covers a
valid fresh admission, exact retry before mutable reads, and CI-principal
rejection; concurrent requests and production drivers remain open.

After adding the admission gate, the full release model reaches 458 states and
1,663 steps; its 18 listed invariants and deadlock check pass. Its generated
chart and report were regenerated from the checked-in spec and view.

## Runtime test evidence

The API module command `cd v2/api && go test ./... -count=1` passed. The
environment did not have an etcd test endpoint, so endpoint-gated integration
tests were skipped in that run. Four focused etcd tests were then run against
a disposable local etcd and passed:

```sh
NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:12379 go test ./etcdstore \
  -run 'Test(ResolveWorkloadReleaseQualificationRequiresSignedCompletedSource|V3PrivateWorkloadReleaseAcceptanceIsAtomicReplaySafeAndKeyRecoverable|V3WorkloadReleaseStagedTargetBindingIsClaimFencedAndWriteOnce|V3WorkloadReleaseReceiptCASFencesAndRecoversEtcd)$' \
  -count=1
```

This verifies the signed qualification lookup, atomic private-input acceptance
and replay, receipt CAS, and successor-claim target binding against real etcd
transactions. PR #321 separately exercises the authenticated admission route
in-process using deterministic fakes. Neither test runs ingress or a
production Nomad/ingress driver configuration.

The updated private-input acceptance test also deletes the qualified source
deployment projection, supplies an invalid current plan and a new operation
ID, removes current qualification trust, and changes current SOPS bytes before
retry. Against disposable etcd, the same stable request returns the stored
acceptance without another lineage read; changed request digests for
qualification or target size conflict.

## Checked behavior

The model covers a crash after reserving the stable stage effect but before
recording the one-use create attempt. The successor claim recovers that
reservation and can mark the attempt once. It also allows a successor to bind
an exact matching job after a lost create response. It holds when the attempt
marker exists but the job is absent, or when the job identity does not match.
A delayed create may still arrive after the controller holds, but the old
workload remains active and no route change or stop follows from that late
registration.

In code, the model's create permission and matching-target observation map to
durable runtime boundaries. The workload-release effect ledger validates a
stage reservation against the signed plan and shared app gate;
`MarkWorkloadReleaseStageTargetAttempt` grants one create attempt under the
live claim. `BindClaimedWorkloadReleaseTarget` then reloads the signed plan,
requires that attempt marker, derives the create-only Nomad lookup from its
target identity and job digest, calls `LookupDeploymentJobRevision`, and only
then writes the claim-fenced binding. The binding, successful stage-effect
completion, and app-effect-gate release share one etcd transaction.
The etcd integration test verifies recovery of a stable reservation before
the attempt marker, one-use marking by the successor, rejection of not-found
readback after an ambiguous create, binding of the exact target under the new
claim, and refusal to let the stale claim write. It also checks that the
lookup uses the signed target with expected modify index zero and that a
conflicting later revision cannot overwrite the first binding. The model
abstracts this observer as a trusted exact readback; it does not model the
Nomad HTTP request or a compromised observer implementation.

The worker orchestration unit test covers the matching recovery sequence at
the registration boundary: the first create response is lost and readback is
absent, then a retry sees the exact target. It verifies that the one-use marker
does not permit another CAS registration and that registration and readback
use the same pinned request. The etcd integration test remains the evidence
for durable successor-claim binding and write-once behavior.

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
atomically. A matching request digest returns the original signed acceptance
before opening current SOPS/source data or observing Nomad; the stored
encrypted snapshot remains the input for recovery. A key inventory reports
all retained IDs. Startup preflight checks configured keys against retained
IDs; deletion/retention policy is not yet implemented. The authenticated
request producer that computes this digest is implemented by PR #319. The
model abstracts its HTTP identity and mutable evidence lookups; handler-level
tests cover fresh acceptance and exact retry, while concurrent admission,
worker/controller restart, and private-input deletion/retention remain open.

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

An earlier 2026-11-08 run checked 460 distinct states and 1,668 transitions.
The current model adds a separate serving-lineage prerequisite alongside the
active route predecessor and checks 458 distinct states and 1,663 transitions.
It includes abstract signed-plan acceptance, request-key replay of the original
plan, app mutation fencing, and route reservation. Five added invariants check
that acceptance carries both fences, replay retains the original acceptance,
a competing app writer cannot enter while the fence is held, and the writer
attempt is accounted for. The stage reservation is now explicit, with an
invariant that the create attempt cannot precede it. The model also corrects
the response-loss action so scheduler acceptance and the lost response occur
in the same transition. All 18 listed invariants and deadlock checking pass.
The chart draws 20 reader-facing states and 43 arrows; 1,125 transitions
stay within a displayed state and are hidden. Request-key replay edges are
also hidden in the chart; the invariant still checks replay identity. The
model treats acceptance and fencing as one atomic abstract step. It does not model etcd compare revisions,
conflicting request fingerprints, route-writer internals, or exercise real
handlers against the modeled steps. Those remain runtime integration and code
review obligations.

The generated [statechart source](https://github.com/antiartificial/norn/blob/877ad4ba/docs/v3/statecharts/workload-release-admission/chart.mmd) and [TLC report](report.md) show the
checked transition graph and result. The conservative hold after an
attempt-marker crash is deliberate. A future retry protocol needs separate
Nomad-version, ACL, retention, and delayed-request qualification.
