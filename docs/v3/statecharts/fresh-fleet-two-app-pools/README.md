# Fresh Fleet with two app pools: create and recover

This is an offline protocol model for a future fresh-Fleet default with two
independent application pools. Current `staging/nyc3` has one app pool of four
Droplets; this model does not change or describe that current pool. Pool A and
pool B are fixed as general and stateful, respectively; provider sizes or
capacity bounds remain unselected. See the [input contract](../../fresh-fleet-two-app-pool-contract.md)
and [staging source audit](../../staging-topology-source-audit-2026-11-08.md).

The model checks each app pool's provider create, response loss, exact readback,
and same-intent recovery before shared inventory, PKI, host configuration, and
terminal receipt. It requires the general workload to be placed on the general
pool and pass readiness, and the stateful workload to be placed on the stateful
pool and pass a recovery/readiness proof before success. Those proof values are
abstract booleans: the model cannot prove actual node counts, identities,
provider API semantics, secret handling, workload-specific data integrity, or
the implementation. It assumes pool declarations passed validation;
rejection before effects is represented as a held branch, not a field-by-field
schema validator.

## TLC result

TLC explored 45 states and 98 steps. Fourteen invariants and the deadlock check
passed. The chart draws 18 states and 26 arrows; it merges role-matched
placement and readiness actions and hides repeated PKI/secrets and terminal
stutter steps.

The checked rules require a bound intent before provider effects, hold unknown
responses, prevent recovery writes before exact readback, prevent duplicate
creates, prevent inventory/configuration from partial pools, enforce
role-correct workload placement and readiness, and prevent a terminal receipt
without stateful recovery proof or when either pool is incomplete or provider
identity differs.

## Reproduce

```sh
/Users/arti/.agents/skills/statechart/scripts/statechart \
  docs/v3/statecharts/fresh-fleet-two-app-pools/FreshTwoAppPools.tla \
  --config docs/v3/statecharts/fresh-fleet-two-app-pools/FreshTwoAppPools.cfg \
  --view docs/v3/statecharts/fresh-fleet-two-app-pools/view.ts \
  --out /tmp/fresh-fleet-two-app-pools-model
```
