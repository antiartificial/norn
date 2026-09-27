# M4 ingress node publisher contract

Status: proposed implementation boundary, not a deployed service or a release
gate. This contract connects the existing canonical route renderer and local
generation-fenced file publisher to Fleet's loopback Traefik readback. The
normal etcd app deployment route stays disabled until the complete path is
qualified.

The source tree now includes `norn-ingress-observer`, a read-only mTLS server
that pins one verified control-client URI SAN and exposes the local managed
file revision plus bounded Traefik `/api/rawdata` from loopback. It refuses
non-loopback Traefik origins, redirects, oversized or invalid JSON, and
unverified clients in local tests. A local binary smoke test reached the
revision endpoint with a trusted client, rejected a missing client
certificate at the TLS handshake, and returned 401 for a validly signed
client with the wrong URI. The candidate build and exact release manifest now
include the binary; no signed release has been published. It has not been
installed on Fleet hosts or connected to the control worker. It cannot
publish or authorize a route, and supplies no public-path or inventory proof.
The control-side `ObservePublishedRenderedRoute` now joins the expected local
file generation/digest with Traefik rawdata on every supplied node and rejects
a generation change during readback. Its node list is caller supplied; durable
Fleet inventory, deployment-bound credentials, endpoint probes and
public-path proof are still required before any terminal traffic result.
`ObservePublishedRenderedRouteWithTLS` now rejects plaintext and non-private
node origins (loopback is accepted for local fixtures) and
uses a supplied CA and client certificate to make mutual-TLS readback requests.
Local tests reach a server that verifies the client URI and reject an untrusted
server CA. This is a control-side transport primitive, not worker integration
or proof that the supplied node set equals Fleet inventory.
Fleet draft PR #177 now emits a private, Terraform-derived ingress host
snapshot with a digest in its hook evidence. Norn's
`ParseFleetIngressInventory` checks exact snapshot bytes, cluster,
environment, canonical schema and private host membership; the
`ObserveFleetIngressInventory` helper uses every derived host for mutual-TLS
file/Traefik readback. The Fleet runner now checks the snapshot against its
configured node source and includes both the snapshot and
`ingressInventoryDigest` in a successful, attempt-bound `nodes_configured`
checkpoint. The control API checks their canonical digest and node membership.
Older checkpoints without these fields remain valid history but cannot supply
M4 ingress inventory proof. `CurrentFleetIngressInventory` now resolves only
the latest successful Fleet attempt with matching configuration and signed
terminal-predecessor checkpoints and one provider state serial; a newer pending
attempt fails closed. The runner's revision-fenced transition to `succeeded`
now atomically selects the active ingress inventory for its cluster and
environment when that attempt carries a valid inventory. The server-owned
active-inventory readback refuses an unrelated plan and carries the pointer
and cluster-epoch revisions for later route-intent comparison. A later
successful plan without ingress evidence withdraws existing pointers for its
cluster in that same terminal transaction; an equal provider state serial is
accepted only when the ingress inventory digest is unchanged.
The result includes the plan-state and checkpoint etcd revisions needed for
terminal fencing. The control worker must bind that Fleet plan and attempt to
its accepted deployment, compare those revisions again at completion,
and reject host replacement between observation and deployment completion.
`ObserveCurrentFleetIngressRoute` now reads the server-selected active inventory, performs
mutual-TLS file/Traefik readback on every member, and rereads inventory to
reject a replacement or active-pointer revision change during observation. This result is not a terminal
traffic proof: app endpoint probes, public load-balancer behavior, accepted
deployment binding and the completion transaction are still outstanding.
`ObserveCurrentFleetIngressTraffic` now composes that readback with an HTTPS
app endpoint probe on each exact private ingress IP and a normal public DNS
probe, then repeats every-node route readback and rechecks the active Fleet inventory and cluster epoch. The expected response digest and
probe path are explicit inputs. This remains an observation until the worker
binds them to the accepted deployment and records a durable, revision-fenced
proof before positive active weight.

## Authority and host boundary

- Fleet must install one narrowly scoped publisher on every ingress host. It
  writes only `norn-route-<32 lowercase hex>.yaml` below
  `/etc/traefik/dynamic`; the existing TLS and readback files are outside its
  write scope. The service must run under an identity authorized to write this
  directory, with no provider, OpenTofu, or general Nomad credentials.
- Norn's accepted deployment and live operation claim are the source of route
  intent. A caller cannot supply an unbound hostname, backend, weight, route
  generation, or withdrawal. The publisher authenticates the caller and checks
  the accepted intent plus current operation authority before each local
  mutation. A bearer token, SSH access, or a file generation alone is not
  sufficient authority for app traffic changes.
- The transport between the control worker and every ingress host must be
  private and mutually authenticated. Fleet must pin each host identity and
  install a separate publisher credential; the existing loopback
  `/api/rawdata` router remains local to each ingress host. A remote caller
  cannot use that unauthenticated loopback route as a control endpoint.

## Publish transaction

1. Resolve the exact accepted deployment, route hostname, revision-specific
   Consul backend names, desired weights, and current route generation from
   durable control state. Compare the live claim and app lock before issuing
   a generation. Record the desired route digest and generation durably before
   fanout, so retry uses the same identity after an uncertain reply.
2. Send the same canonical route and generation to the complete ingress node
   set from one exact Fleet inventory revision. Each host invokes
   `PublishRenderedRoute` with its previously read local revision. A replay of
   the identical generation and digest is idempotent; a stale or divergent
   revision is a conflict. A withdrawal uses
   `WithdrawPublishedRoute` and retains its tombstone generation.
3. Read each host's published file revision and Traefik's effective
   `/api/rawdata`. Require the exact router, weighted service, TLS mode,
   enabled backends, and weights on every node. Probe the expected app endpoint
   through every ingress node and through normal public DNS. Include a
   withdrawal/public 404 check for rollback. A partial publish, unavailable
   host, competing public router, config error, probe mismatch, or unknown
   Fleet inventory member leaves active weight at zero and retains the app
   gate for recovery.
4. Persist one proof tied to deployment ID, region, accepted intent digest,
   route digest and generation, Fleet inventory revision and complete node IDs,
   per-node file/readback/probe results, and public probe result. The terminal
   deployment transaction must compare this proof's durable revision and
   authority before it can write a positive `ActiveWeight`. The current
   `finishClaimedDeployment` caller-supplied region value is insufficient.

## Accepted route binding and completion fence

The private etcd `finishClaimedDeployment` path now rejects every positive
`ActiveWeight` until a deployment-bound ingress proof is durably compared in
its terminal transaction. Zero-traffic completion still exercises the claim,
app-lock and admission transaction in disposable etcd tests. This is a safety
fence, not the missing proof implementation or normal app execution.

The InfraSpec endpoint now has an optional `process` binding, validated against
a declared service process with a port and included in the pinned spec digest.
The Fleet pilot spec binds its endpoint to `web`. Existing Mini specs may omit
the field, but Fleet route qualification must reject an absent binding rather
than guess a process. The claimed Fleet route-source resolver now verifies the
signed deployment's spec digest, accepted region and weight against that exact
spec, unique regional endpoint, service process, and HTTPS origin before
returning route inputs. Its result carries the signed control environment,
matched operation and deployment IDs, signed acceptance ID, and canonical
digest required for the route-intent record. A durable route-intent path does
not consume this result yet.

The current `Deployment` and `ResolvedRegion` records identify the app,
deployment, region, and desired regional traffic weight. They do not identify
the public endpoint, process, previous deployment backend, or a complete
old/new route split. `RenderedRoute` is presently a caller-supplied value.
Passing that value through observation cannot authorize terminal traffic.
The deployment acceptance still does not bind this app to a Fleet cluster and
environment. `CurrentFleetIngressInventory` accepts a caller plan ID for
diagnostic readback; the route-intent worker must use the server-owned active
pointer and bind the accepted deployment to its cluster/environment. The
intent transaction must compare that pointer and its current checkpoint. The
app's prior route pointer must likewise be loaded from durable state, not
supplied by the request.
The proposed [Fleet app route authority ADR](adrs/0008-fleet-app-route-authority.md)
specifies the control-owned app target binding and prior-route pointer for
the first v3 Fleet. It remains a proposal until review and implementation.

The worker must create a durable route-intent record before any publish. It
must contain a schema version; app, operation, deployment and region IDs; the
signed acceptance's canonical digest and pinned InfraSpec digest; endpoint
origin and process from that exact InfraSpec; the full old/new deployment ID
and weight set; the canonical route SHA-256; Fleet plan ID, completed attempt
ID, provider state serial, ingress inventory digest and member IDs; the
previous route generation; and the new generation. The worker derives this
record while holding the deployment claim and app lock. It resolves the old
backend from the current durable route state and verified revision job, not
from a request parameter. It must refuse a missing or changed source spec,
unverified old backend, ambiguous endpoint, or a weight plan whose regional
and revision semantics are not explicitly represented.

The first intent write compares the signed acceptance, app gate, operation
claim, current route revision, and Fleet plan/checkpoint revisions in one
control transaction. A retry after an uncertain response loads the existing
record and accepts only byte-identical identity, inputs, route digest and
generation. It never allocates a fresh generation merely because the first
response was lost. Every node publication carries this persisted generation
and digest; the ingress host independently checks its local predecessor.

After every node and public probe passes, a separate durable proof records the
intent ID and revision, every observed node identity and file/Traefik
revision, endpoint response identity, public response, and observation time.
The terminal transaction compares the live claim/app lock, the signed
acceptance, intent and proof revisions, and the still-current Fleet plan and
checkpoint revisions. It also compares the current app route pointer to the
proved generation. A replaced host, changed route, expired claim, or
incomplete observation leaves the operation and app gate unresolved with
`ActiveWeight` zero. The transaction derives the active weight from the
proved route-intent backend set; callers cannot supply a positive value.

Required failure tests: a forged endpoint or backend with matching Traefik
readback; a lost intent-write response; a one-node publish interruption;
Fleet host replacement between node and public probes; a route generation
change after proof persistence; and a lost claim immediately before terminal
commit. Each must refuse positive weight and preserve recovery authority.

## Recovery and qualification

After a lost response, reread durable intent and each node's file/effective
route before retrying the same generation. Never issue a newer generation or
release the app gate while an older publish may still be active. If nodes
disagree, repair or withdraw under a newer authorized generation, then repeat
all readback and probes. A process crash after the last node write but before
proof persistence must be reconciled by observation, not a blind second route
change.

The first protected test needs two separate ingress hosts, one route revision
at a time, old/new revision-specific backends, a public load balancer, an
induced one-node publish failure, and an interrupted publisher recovery. The
test must demonstrate that no positive `ActiveWeight` or terminal success is
recorded until every node and the public path agree. Local Traefik fixtures
and Fleet draft PR #177 establish only composition and loopback readback.
