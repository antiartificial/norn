# M4 ingress node publisher contract

Status: proposed implementation boundary, not a deployed service or a release
gate. The source tree has a node publish handler that accepts only a reserved
intent ID from a pinned mTLS client and requires a trusted live-authority
resolver to supply the exact route, predecessor and generation. A separate
control authority handler binds the node ID to a verified client URI; the
host-side client pins both the control CA and server URI. A local mTLS test
feeds its decision through the node publisher and checks the resulting file
revision. The private store decision can serve that handler while its worker
holds a live claim, app lock, and completed deployment-bound Nomad job-health
effect. An accepted route intent alone cannot authorize publication. The
`norn-ingress-publisher` host binary uses
separate inbound control and outbound node mTLS identities, rejects a
non-private listen address, and is required in candidate release manifests.
An opt-in etcd deployment worker now opens this listener for a claimed release
and drives the publisher path. The Fleet host service is staged disabled in
draft PR #177; there is no protected operational publication yet.
The control-side private mTLS client now sends only the reserved intent ID to
every prevalidated private publisher origin and requires each host's immediate
file-revision receipt. On a failed or uncertain request it returns the receipts
already confirmed; the current or failed host may still have published and
must be reconciled by readback. The opt-in executor invokes this client after
Nomad health. Its receipts alone do not establish effective Traefik or public
traffic.
The active Fleet inventory's node URLs address the read-only observer. The
normal executor now requires a distinct `publisherPort` and derives publisher
origins from those exact private inventory IPs; it keeps the observer URLs for
readback. Without this separation, fanout would send `/v1/routes/publish` to
the observer port and could not complete the route transaction.
Before staging managed inputs or submitting a Nomad job, the executor now
requires an authenticated `GET /v1/health` response carrying the exact node ID
from every active inventory publisher. The health route requires the pinned
control mTLS identity and does not call route authority or mutate a file. It
proves the private service can be reached at preflight time, not that later
publication or public traffic will succeed.
This contract connects the canonical route renderer and local
generation-fenced file publisher to Fleet's loopback Traefik readback. The
normal etcd app deployment route remains opt-in pending qualification.

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
`ObserveClaimedInitialFleetRoute` now derives the expected route and members
from the signed, live claimed intent, requires its completed Nomad health
effect, and rechecks the intent after readback. A disposable-etcd test rejects
an inventory replacement during that interval. This is still file/Traefik
observation; it does not prove app endpoint or public traffic.
`ObserveCurrentFleetIngressTraffic` now composes that readback with an HTTPS
app endpoint probe on each exact private ingress IP and a normal public DNS
probe, then repeats every-node route readback and rechecks the active Fleet inventory and cluster epoch. The expected response digest and
probe path are explicit inputs. This remains an observation until the worker
binds them to the accepted deployment and records a durable, revision-fenced
proof before positive active weight.
`RecordClaimedInitialFleetTrafficProof` now derives the route and exact probe
path/response digest from the live signed InfraSpec intent, performs those
probes, and stores an immutable observation with
claim, app-lock, acceptance, effect, route-intent, target and Fleet inventory
revision fences. The proof is keyed by claim generation, so a successor claim
must observe traffic again. An identical fresh observation can replay an uncertain write;
a conflicting observation cannot overwrite the receipt. The Fleet pilot's
readiness-gated `/route-proof` response is pinned by its InfraSpec digest.
The private terminal path now reloads this record, its signed route intent,
completed Nomad health effect, current Fleet target and inventory, and
compares their revisions in the same transaction as positive active weight.
A disposable-etcd fixture proves the control path with a synthetic traffic
observation. Protected ingress and public load-balancer behavior remain to be
qualified.

## Authority and host boundary

The current normal etcd API exposes managed bearer-token routes on its
existing listener; it has no ingress-host mTLS authority route. Do not point
the node publisher at that listener or provision a general Norn API token on
an ingress host. The deployable path needs a separate private mTLS control
listener (or an equivalently isolated control service) with a pinned ingress
node client identity and one read-only authority operation. For the supplied
intent ID and node ID, that operation must reload the durable reservation,
signed deployment acceptance, pinned route identity, live operation owner
lease and app lock, control-owned Fleet target, active ingress pointer and
cluster epoch, and the exact member set. It returns the canonical route,
predecessor revision and generation only when the named node is a member of
that current inventory. The host then rechecks its local predecessor and
writes the file. No response may be cached across publication attempts.

The control worker must retain the app gate and repeat the durable-authority
check after host readback and before terminal positive weight. The authority
response alone cannot make a cross-system file write atomic with etcd; a
claim loss or host replacement between the response and write is handled by
fail-closed terminal proof and reconciliation of every node. Qualification
must exercise that race and show that traffic is never marked active from a
stale response.

Publishing a public Traefik route can serve real requests before etcd records
terminal `ActiveWeight`. Zero in the control record is not a network traffic
fence. The accepted deployment must already be ready, and any source-writer
fence required for a cutover must precede publication. A partial publish
requires explicit node repair or withdrawal under the retained app gate;
terminal proof cannot retroactively prevent requests routed during that gap.
The first-route rehearsal must observe this interval and prove the chosen
recovery behavior before M4 sign-off.

The remaining qualification order is: supply a completed active Fleet ingress
inventory and narrowly scoped host credentials; exercise the opt-in normal
worker from accepted release through private publication, every-node and
public-path readback, durable proof, and terminal completion; then rehearse
partial publish, claim loss, replacement, and loaded 2→3→2 Fleet operation.
The draft host publisher service remains disabled pending that qualification.
The worker's source-loading helper now selects one enabled local InfraSpec and
checks its digest, region, endpoint and Fleet target against the signed claimed
deployment. It rejects duplicate app documents and changed or disabled source.
A pure job-plan step translates
that source into the managed Nomad revision, stamps signed database target
provenance and a deterministic submission-effect identity, and passes the
existing effect boundary's exact-job validation. It does not stage Nomad
variables, submit the job, or publish ingress. The next private step stages
and reads back managed Nomad inputs, then rejects a prepared job that differs
from that signed plan. Managed revision translation now omits the legacy
wall-clock deploy marker and sorts task groups; retries reconstruct the same
job digest and effect identity. The private Nomad step now carries the effect
token through submit readback and healthy allocation evidence; pending health
defers without resubmitting, and a successor claim can reuse a completed
health effect. A claimed route step now reserves the signed first-route intent,
fans its ID to every current ingress node, records fresh file, Traefik,
endpoint and public-path proof, then invokes the proof-gated completion. Any
failure after fanout returns a deferred result with partial receipts so the
app admission hold can preserve recovery authority. A disposable-etcd test
covers partial publication and successful synthetic proof. The opt-in normal
worker now owns the private mTLS authority listener and composes these steps.
The disposable-etcd successor-claim rehearsal defers after a partial publish,
then confirms the previous claim loses node authority while a new claim can
reuse the exact durable route intent and completed Nomad health effect. The
new claim has no inherited traffic proof or active route; it must publish and
observe again. This is claim recovery evidence, not protected host repair.
Its disposable HTTP-to-Nomad rehearsal rejects a release without completed
active ingress inventory before opening that listener, then directly
demonstrates a healthy Nomad allocation while remaining nonterminal without
ingress proof. Protected Fleet publication, real artifact verification,
public traffic, partial-publish repair, scaling, and drain under load remain
unqualified.

- Fleet must install one narrowly scoped publisher on every ingress host. It
  writes only `norn-route-<32 lowercase hex>.yaml` below
  `/etc/traefik/dynamic`; the existing TLS and readback files are outside its
  write scope. The service must run under an identity authorized to write this
  directory, with no provider, OpenTofu, or general Nomad credentials.
  The file primitive accepts a group-writable directory only when root owns
  it and the sticky bit is set; it rejects world write. Fleet must use a
  separate publisher account and group, keep TLS/readback files owner-only,
  and grant Traefik read access to generated route files. The publisher must
  not run as the Traefik account, which holds separate Consul and TLS keys.
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

The private etcd `finishClaimedDeployment` path rejects positive
`ActiveWeight` unless its caller supplies the pinned InfraSpec and observer
port and a durable deployment-bound ingress proof passes live validation. The
exported `CompleteClaimedInitialFleetDeployment` entry point derives the
deployed result and 100-percent regional weight from signed acceptance, so a
worker cannot choose a different active weight. The
terminal transaction compares the claim, app lock, acceptance, intent, proof,
completed health effect, Fleet target, active inventory and absent prior active
route before writing the active route pointer and region result together.
Disposable-etcd tests cover synthetic success, missing or corrupt proof, and
inventory replacement. Normal app execution and protected traffic proof are
still outstanding.

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
The private etcd acceptance path now snapshots a generation-fenced Fleet app
target and verifies it against signed request semantics under a target-revision
CAS. Its writer remains private; route intent and prior-route authority are
still absent. The claimed route-source resolver carries that verified target's
Fleet cluster, environment and generation for a future route-intent transaction.
The unique HTTPS endpoint/process resolver now lives in the shared model
package, so that transaction can rederive source values from the pinned
InfraSpec rather than trust worker-supplied endpoint fields.
The first-route private etcd transaction now persists a generation-one intent
and reservation against the current active Fleet pointer. It also writes an
intent-ID index in the same transaction so a future authority service can
resolve the host's opaque request ID; retry checks the index against the
reservation and route record. It has no operational network authority resolver,
publisher, or terminal proof path and cannot activate traffic. Old/new split transitions
still require the durable prior route pointer described below.
The private `AuthorizeInitialFleetRouteForNode` method now rechecks a held
claim, app lock, signed acceptance, exact intent ID and current Fleet
inventory before returning the generation-one route for one named member. It
also reloads the completed Nomad job-health effect and recorded submit attempt
for that deployment, then rechecks route authority. The completed effect proves
one observed healthy allocation state, not continuing app or public-route
health; endpoint and public probes remain necessary after publication.
`NewClaimedInitialFleetRouteAuthorityHandler` connects that decision to the
certificate-bound control handler while the worker holds its claim and pinned
InfraSpec. A disposable-etcd test authorizes the named member and rejects the
same request after Fleet inventory replacement. No runtime mTLS listener or
host-to-control call invokes this handler yet; recovery must reacquire the
claim, lock and pinned source before it can serve again. A private-IP-only
mTLS listener primitive now stops on worker-context or app-lock cancellation;
the normal etcd deployment worker does not start it yet. A disposable-etcd
integration test now carries the signed generation-one intent through this
claimed listener, node mTLS client, and local file publisher, then rejects
a new authority request after the Fleet inventory epoch changes. This is a
local path test, not a protected host or public traffic proof.

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

The normal etcd runtime starts a dedicated `app.deploy` worker only when
`NORN_ETCD_FLEET_DEPLOY_WORKER_CONFIG` names an absolute, owner-only JSON file.
The file declares `bind` (a private literal IP and fixed port), `nodeUris`
(mTLS URI SAN to ingress node ID), separate `observerPort` and `publisherPort`,
`endpointPort`, and absolute
owner-only PEM paths: `authorityCertFile`, `authorityKeyFile`, `nodeCaFile`,
`publisherCaFile`, `publisherCertFile`, `publisherKeyFile`, `observerCaFile`,
`observerCertFile`, and `observerKeyFile`. `publicCaFile` is optional; without
it public HTTPS probing uses system roots. Startup also requires the active
`NORN_DATABASE_PROFILE`, private `NORN_DATABASE_SECRET_DIR`, app catalog,
control authority, and Nomad address. It validates the bind and all mTLS
material before the worker claims an operation. Each claim binds and owns its
own authority listener, and preflights it again before the Nomad effect.
There is still no normal producer for signed `app.deploy` aggregates:
`V3OperationStore.Accept` refuses deployment admission and the specialized
aggregate acceptance is private to etcdstore. The opt-in worker therefore
cannot deploy an app in a fresh normal runtime yet. A reviewed admission path
must derive the deployment, regions, database targets, control-owned Fleet
target and release provenance from server-verified sources, then atomically
accept them under the app gate. The control-owned target can now be configured
separately by a non-CI platform operator with an expected revision and a
validated Fleet document. Admission must not expose a raw acceptance object
as an HTTP body.

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
