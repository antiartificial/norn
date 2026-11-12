# Fresh Fleet pre-registration authority model

This bounded model defines the Norn authority boundary needed for fresh Fleet
creation. It does not call a provider, run OpenTofu, or enable an API route.

## Finding from the current implementation

The current executor attempt API is not usable for the initial provider
create. `CreateFleetExecutorAttempt` requires `loadFleetTargetFenceContext`
to resolve an already registered target, and refuses when the target registry
is empty. `RegisterFleetTarget` creates the target and free fence only after
provider identities are known. The existing app-delete co-permit is also
deliberately tied to a live registered-target executor attempt. Therefore,
adding a create-shaped request to that co-permit would not authorize the
actual pre-registration create; it would either be unreachable or weaken the
registered-target fence contract.

## Required Norn lifecycle

The model describes the required authority handoff:

1. Bind one immutable create intent to the reviewed plan, provider account,
   state lineage/serial, topology, and SSH identity inputs.
2. Bind the exact protected workflow dispatch to that intent before starting
   the pre-registration runner attempt.
3. Record verifier admission for the exact attempt and observed remainder
   before consuming a one-use create grant. A lost response cannot replay the
   consumed grant.
4. On restart, require exact provider and state readback before admitting a
   successor attempt for the same intent; record a new verifier admission for
   that successor and exact remainder.
5. After all nine node identities and state lineage match, atomically register
   the Fleet target and hand the cluster creation fence to its ordinary target
   fence. A partial or mismatched inventory stays held and cannot bootstrap.

The provider and state observations remain verifier assertions with opaque
digests from Norn's perspective. A create-specific Norn grant proves Norn's
intent, attempt, and fence binding; it does not by itself prove provider account
scope or state correctness. The independent Fleet verifier must establish
those facts before asking Norn to record admission.

## Reproduce

Run from this directory, using a new output directory:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/FreshFleetNornAuthority.tla" \
  --config "$PWD/FreshFleetNornAuthority.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

TLC explored 19 states and 21 steps. Nine invariants and deadlock checking
passed: type safety, grant-before-effect, consumed-grant-was-issued, exact
remainder binding, protected dispatch-before-attempt, attempt-specific
admission-before-grant, same-intent recovery, registration-after-terminal-
receipt, and no target during create. The model
abstracts the protected dispatch as an exact bound boolean, admission as an
attempt number, nine resources to counts, and provider/state readback as one
exact observation. It does not model an etcd transaction, signatures, lease
expiry, two competing create intents, credential scope, or live effects. The
chart collapses simultaneous provider and Norn state changes into labeled
parallel regions; it shows grant-response recovery, verifier recovery-readback
before a successor, an explicit complete terminal receipt, and target/fence
transfer. The model abstracts the atomic handoff as one action; the real-etcd
test below exercises that transaction.

## Implementation acceptance

The typed shape contract is in `v2/api/fleet/fleetcreate/effect.go`; its
offline tests cover input digests, intent/admission linkage, exact remainder
grant matching, and strict request decoding. `V3OperationStore.RecordFleetCreateIntent`
now writes the immutable intent and cluster fence atomically in etcd, after
checking the accepted capacity-plan identity. Target registration includes
the create-fence prefix in its read/CAS boundary and refuses a cluster with an
active pre-registration intent. Focused etcd tests cover exact replay,
substitution, registered-target refusal, and competing intents.

The durable slice now includes intent, verifier-owned protected-dispatch
binding, initial-attempt, and verifier-admission records. Dispatch binding is
write-once per intent and carries repository, workflow path, environment, run
ID/attempt, commit, and receipt digest. Attempt start compares its runner
identity and receipt digest to that binding and CAS-checks the binding revision
with the cluster fence and plan. Exact retries return the same records.
Admission is write-once for the exact intent, attempt, cluster, and state
lineage; its digest-checked readback supports exact replay and rejects changed
or competing admissions. The store also issues an exact-remainder grant only
when the current fence, dispatch, attempt, admission, and accepted plan all
match. Issue and consume have immutable events and etcd compare-and-swap;
readback distinguishes issued from consumed after response loss. Real-etcd
tests cover exact issue replay, changed-remainder refusal, one-use consumption,
readback, competing consumers, exact successor admission, and response loss
after committed recovery-readback and successor-start writes. The full
`go test ./...` API module suite passed after these changes.

This still is not the complete effect path. Default-off protected routes now
record/read intent and dispatch binding, start/read an attempt, and
record/read verifier admission and terminal evidence. A separate verifier
route requests the target/fence handoff. The terminal write requires the
current attempt, exact admission, consumed grant, accepted plan, and complete
nine-node count. Handoff reuses ordinary alias and registry preparation and
atomically creates the target/free target fence while retiring the same
intent's pre-registration fence. A real-etcd test covers partial-inventory
refusal, lost terminal-write and handoff responses, replay, alias registration,
ordinary target-fence readability, and create-fence retirement. Attempt
start compares GitHub OIDC repository, workflow, environment, run, attempt,
commit, protected ref, and event claims with the immutable binding. Norn still
trusts the dedicated verifier's GitHub run readback assertion; it does not
independently query GitHub. The receipt digest is a verifier
assertion, not proof Norn independently checks. The stores do not prove process
liveness or runner retirement. Successor recovery is qualified only within
the existing verifier-bound GitHub run; a fresh protected dispatch/rerun
binding after runner loss remains open. Default-off grant issue, consume, and
non-capability status handlers are now locally covered by focused route,
real-etcd, and full-module tests. Ambiguous consumption is reconciled through
runner-bound status readback, which returns only the grant digest and consumed
flag without returning the grant record as another successful consume.
Provider-executor and protected-workflow integration remain open. The
statechart is not an implementation proof. No provider effect has run.

Remaining implementation and qualification must define and secure how the
verifier's GitHub run readback is validated; cover
concurrent competing intents, dispatches, and admissions;
ambiguous commit replies; stale attempt/lease; wrong account/backend/serial;
changed remainder; consumed-grant replay; a lost first apply response; partial
and extra inventory; controller restart; and fresh dispatch recovery after
runner loss. Existing
registered-target executor behavior and app-delete authorization must remain
unchanged.
