# Simultaneous HTTP workload release admission

This bounded model adds the HTTP handler interleaving that the store-level
concurrent-admission model abstracts away. Two handlers can both miss the
initial stable-key lookup and read mutable qualification, policy, route, and
source evidence. The model then treats `AcceptPrivateWorkloadRelease` as one
atomic store boundary: one caller creates the durable acceptance and the
other receives that acceptance as a replay. It checks that there is at most
one fresh operation and that both successful callers identify that same
operation.

## Regenerate

From the repository root:

```sh
~/.agents/skills/statechart/scripts/statechart \
  docs/v3/statecharts/workload-release-http-concurrency/AdmissionHTTP.tla \
  --config docs/v3/statecharts/workload-release-http-concurrency/AdmissionHTTP.cfg \
  --view docs/v3/statecharts/workload-release-http-concurrency/view.ts \
  --out /tmp/norn-workload-release-http-concurrency
```

The checked config reaches 19 states and 28 transitions. `TypeOK`,
`AtMostOneFreshAcceptance`, `FreshAcceptanceHasStableSelection`,
`EveryReplayReturnsAcceptedSelection`, `EveryFreshResultHasDurableAcceptance`,
`BothSuccessfulRequestsConverge`, and deadlock checking pass. The chart hides
four redundant actions and six transitions that stay within one displayed
state.

## Runtime correspondence and limits

`TestEtcdWorkloadReleaseAdmissionConcurrentSameKeyConverges` starts two real
HTTP handler requests together and holds both at the initial replay miss. Both
then traverse the trusted-evidence path and call the acceptance boundary; the
test checks one fresh response, one replay response, and the same operation ID
in both. It uses a deterministic fake store whose acceptance boundary models
one atomic winner. Separately,
`TestV3PrivateWorkloadReleaseAcceptanceIsAtomicReplaySafeAndKeyRecoverable`
uses two independent etcd clients to prove the real store boundary returns one
fresh acceptance and one replay under the same race.

Run the HTTP interleaving test with:

```sh
cd v2/api && go test -race . \
  -run 'TestEtcdWorkloadReleaseAdmissionConcurrentSameKeyConverges' -count=1
```

These two proofs cover adjacent boundaries, not one end-to-end HTTP-to-etcd
test. The model assumes atomic store acceptance; the HTTP test uses a fake at
that boundary, and the etcd test does not pass through the HTTP handler. It
also abstracts qualification freshness and concurrent reads, process restart,
private-input rotation/deletion, worker recovery, Nomad/ingress drivers, and
live workload behavior. Those gates remain open.

An initial disposable-etcd HTTP harness was discarded because it tried to
seed an already-succeeded qualification deployment directly. The real store
requires that source to have completed signed deployment and ingress-route
admission, so direct etcd writes would make the concurrency result misleading.
The replacement needs to create that source through the real route-completion
path; only the Nomad lineage observer may be substituted. See the dated
fixture-boundary note in the M5 workload-release protocol.
