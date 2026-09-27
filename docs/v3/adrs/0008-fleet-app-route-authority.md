# ADR 0008: Fleet app target and route authority

Status: proposed, 2026-09-27. This is the missing M4 execution contract; it
does not enable the normal etcd deployment route or authorize live traffic.

## Context

The accepted deployment identifies a Norn control environment, region and
desired traffic weight. Its pinned InfraSpec now identifies the HTTPS origin
and service process. Fleet has a server-selected active ingress inventory for
a cluster and Fleet environment. No durable app-to-Fleet binding joins these
authorities. A caller can still name a cluster for diagnostic readback, while
the private completion transaction correctly rejects positive active weight.

## Recommended decision

Maintain a control-owned, revisioned app target binding keyed by Norn authority,
app and control environment. Its value names the Fleet cluster and Fleet
environment, the intended Nomad region/datacenters, and a binding generation.
Only an authenticated operator configuration operation may create or change it;
deployment requests and InfraSpecs cannot select a cluster. The first v3
Fleet can have one binding per app, but this must be an explicit record rather
than an inferred default. Existing Mini deployments have no Fleet binding and
continue on their PG profile.

At etcd deployment acceptance, load the binding while holding the app
admission gate. Copy its identity and digest into the signed acceptance, and
compare its revision in the admission transaction. An absent, ambiguous or
changed binding fails admission. The signed placement must match the binding's
Nomad target. This makes retry resolve the same target without consulting a
new default. A later binding change cannot redirect an already accepted
deployment.

At route-intent creation, load the signed deployment, pinned InfraSpec and
binding, then select the current active Fleet ingress inventory for exactly
the binding's cluster and environment. The Fleet plan is selected by the
server-owned active pointer, not by a deployment request. Compare the binding,
active pointer, cluster epoch, plan and checkpoint revisions in the same
transaction that records route intent. If Fleet replacement changes any of
them, stop and reconcile before publishing.

Keep the prior route as a separate durable app/region pointer containing the
previous accepted deployment, backend identity, weight set, route generation
and revision. The route renderer derives old and new backend names from signed
deployments and verified jobs. Neither an API caller nor a Traefik file may
claim the prior backend. Persist the complete intended split and canonical
route digest before sending a generation to ingress publishers. A retry after
an uncertain response must reuse the identical intent and generation.

Positive active weight requires durable proof of every active ingress node's
published generation and Traefik route, exact-node application response and
public response. The terminal deployment transaction compares the proof,
intent, app gate/claim, binding, active Fleet pointer and prior-route revision;
it derives active weight from the proved split. A caller-supplied region weight
is never terminal authority. Partial publication retains the app gate and
allows repair or withdrawal under a fenced later generation.

## Failure behavior and rollout

- Binding missing or changed: refuse route intent; preserve the accepted
  deployment and app gate for explicit recovery.
- Fleet host replacement or plan advancement: invalidate prior observations
  and repeat publication/readback against the new complete inventory.
- Prior route unknown: refuse a positive split; do not guess from the latest
  Nomad job or DNS response.
- Lost write response: read durable intent and every host's generation before
  retry. Never allocate a new generation solely because the response was lost.
- First release: qualify one explicitly bound app and one Fleet cluster.
  Multi-cluster target selection and cross-cluster traffic are later work.

## Acceptance evidence

Show a signed acceptance whose target cannot be changed by replay or a caller,
then a normal etcd app deployment with old/new revision-specific backends.
Interrupt intent persistence, one host's publication, node replacement,
public probing and terminal commit. No interruption may record positive
active weight or release the app gate before the exact bound route is proved.
Rehearse rollback before and after new backend traffic, including public
withdrawal and prior-route restoration.
