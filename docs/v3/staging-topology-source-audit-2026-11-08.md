# DO staging topology source audit — refreshed 2026-11-09

This audit distinguishes the current protected `norn-fleet` main source, an
older local checkout, provider inventory, and unverified runtime health. The
topology match is not proof of node enrollment, workload placement, or service
availability.

## Current protected source

GitHub reports `antiartificial/norn-fleet` main at
`6335c667693d0333d4239f91edf2efb9772ab63a` (including PR #837 and the merged
PR #946 source-admission fix). Its
`environments/staging/nyc3/cluster.yaml` declares three pools:

- 3 control Droplets (`s-2vcpu-4gb`);
- 2 ingress Droplets (`s-1vcpu-2gb`);
- 4 app Droplets (`s-2vcpu-4gb`).

The app pool is one pool labeled `workload: app`; this is not the future
General + Stateful two-pool default. Managed PostgreSQL and MySQL are separate
database services, not compute pools.

The earlier local source audit used `norn-fleet` checkout
`cc2be083a00d6afd3964655519dc2abb580150b6`, which declared only control and
ingress pools. That checkout is behind the protected main source for this
topology and must not be used as the current staging contract. Its state
informs the origin of the stale five-node documentation, not today's source.

## Provider readback

Read-only DigitalOcean API queries through explicit `work` context on
2026-11-09 18:58 UTC found all nine expected NYC3 Droplets active: three
control, two ingress, and four app nodes. Both staging-management Droplets and
the ingress load balancer were active; management-authority PostgreSQL and
application PostgreSQL/MySQL reported online. The current public canary probe
at 19:01 UTC returned the expected route proof and source across two
allocations on two nodes. The default `norn-pilot` context is a different
account; do not use it for staging inventory.

These reads confirm provider-reported resources and a public canary app route.
They do not establish exact protected-state identity, Nomad/Consul health, node
enrollment, full workload health, Norn API health, or a successful platform
release. The latest protected Genesis readback
([42418784411](https://github.com/antiartificial/norn-fleet/actions/runs/42418784411))
failed in the source-capability check after private transport. Its pinned
state and route inputs are from Oct 21, and the log does not identify which
source object is absent. A new readback needs a fresh signed one-use intent
bound to reviewed current inputs after that object availability is diagnosed.

## Reconciliation and next action

The current DO staging Fleet is a 3-control + 2-ingress + 4-app, nine-node
testbed. The nine-node statechart has the same pool counts and useful partial
create/recovery invariants, but aggregates credentials and inputs and does not
exercise the protected provider workflow. It must not be described as a live
recovery qualification.

The agreed General + Stateful two-app-pool default is for a future fresh
Fleet. Its [input contract](fresh-fleet-two-app-pool-contract.md) and
[separate model](statecharts/fresh-fleet-two-app-pools/README.md) do not change
current staging. IDs, sizes, min/desired/max, cost limits, placement, and
workload recovery policy remain open before provider-source changes.

Before any DO write lane, obtain fresh protected state, route, and host-key
receipts; verify private control APIs, etcd quorum, capabilities, workload and
route health; and diagnose source-object availability. Provider inventory is
not a substitute for those gates.
