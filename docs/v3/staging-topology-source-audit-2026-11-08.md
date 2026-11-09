# DO staging topology source audit — refreshed 2026-11-09

This audit distinguishes the current protected `norn-fleet` main source, an
older local checkout, provider inventory, and unverified runtime health. The
topology match is not proof of node enrollment, workload placement, or service
availability.

## Current protected source

GitHub reports `antiartificial/norn-fleet` main at
`35e1c893198efbdc9e407d708e5096a32b485f7d` (including PR #949's repeated
staging-plan readback). Its
`environments/staging/nyc3/cluster.yaml` declares three pools:

- 3 control Droplets (`s-2vcpu-4gb`);
- 2 ingress Droplets (`s-1vcpu-2gb`);
- 4 app Droplets (`s-2vcpu-4gb`).

The current app pool is one pool labeled `workload: app`; this does not change
the future General + Stateful two-pool default. Protected source supports the
future pair in validation, generated inventory, and Ansible/Nomad bootstrap,
but staging's checked-in cluster configuration remains unchanged. Three
focused tests for the two-pool validator, inventory, and bootstrap boundaries
passed against main `35e1c893`. Managed PostgreSQL and MySQL are separate
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
current staging. Stable pool IDs are selected as `general` and `stateful`.
Sizes, min/desired/max, cost limits, placement, stateful recovery policy, and
an integrated protected apply/recovery fixture remain open before a fresh
provider profile or plan exists.

Before any DO write lane, obtain fresh protected state, route, and host-key
receipts; verify private control APIs, etcd quorum, capabilities, workload and
route health; and diagnose source-object availability. Provider inventory is
not a substitute for those gates.
