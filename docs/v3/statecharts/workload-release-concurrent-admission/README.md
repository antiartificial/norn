# Concurrent workload admission and replay model

This bounded model captures two independent store clients accepting the same
stable workload release request concurrently. Both callers can miss the first
replay lookup; one commits while the other encounters the active-app admission
fence. With `RetryAfterFence = FALSE`, TLC produces a counterexample where the
winner returns a fresh acceptance and the loser returns an error. With the
setting enabled, the loser rechecks the stable identity after fence contention
and returns the winner's receipt.

## Regenerate

From the repository root:

```sh
~/.agents/skills/statechart/scripts/statechart \
  docs/v3/statecharts/workload-release-concurrent-admission/ConcurrentAdmission.tla \
  --config docs/v3/statecharts/workload-release-concurrent-admission/ConcurrentAdmission.cfg \
  --out /tmp/norn-concurrent-workload-admission
```

The fixed model reaches 14 states and 18 transitions. `TypeOK`,
`AtMostOneFresh`, and `SameKeyConverges` pass. Deadlock checking is disabled
because terminal result states intentionally stutter. The
pre-fix five-step counterexample is preserved in `pre-fix-counterexample.md`.

## Runtime correspondence and limits

The model counterexample was reproduced by the etcd integration test using
two independent `V3OperationStore` clients. The store now rechecks the durable
request identity when app-fence acquisition fails. A same-request winner is
returned as a replay; a conflicting identity still reports its conflict, and
an absent record preserves the original fence error. The integration test
checks one fresh result and one replay for simultaneous same-key requests.

This is store-level etcd concurrency evidence. It does not exercise two
simultaneous HTTP handler requests, process restart or response loss, the
production Nomad/ingress drivers, or live Fleet behavior. Those remain separate
qualification gates.
