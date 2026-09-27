# M4 local Traefik weighted-route fixture — 2026-09-27

This is disposable local runtime evidence for the opt-in M4 ingress route
components at Norn head `aea72a71ee6012e6a6182bd7e8b961df9228689d`.
It does not apply a route to Fleet or close M4.

## Fixture and result

- The official Traefik v3.7.13 Darwin/arm64 release archive passed its
  published SHA-256 checksum. A loopback Traefik process used a watched file
  provider and a loopback Consul Catalog provider backed by Consul 2.0.4.
- `ingress.RenderWeightedRoute` produced a file-provider router for
  `orders.example.test` and a weighted service referencing two distinct
  revision-specific `@consulcatalog` services: old at 70 and new at 30.
  Consul registered each service with a private `.norn.invalid` router tag.
- Traefik's actual `/api/rawdata` showed the public `@file` router enabled,
  its weighted service enabled at 70/30, and both referenced Consul Catalog
  services enabled. `ingress.ObserveRenderedRoute` accepted that response.
- Two loopback HTTP backends returned distinct `old` and `new` bodies. One
  hundred sequential requests to the public-host rule through local Traefik
  returned **70 old and 30 new**. This is a deterministic local routing
  check, not a statistical production traffic guarantee.
- On initial Traefik startup, the log briefly reported a missing backend
  before Consul discovery completed. The later raw configuration and route
  probes passed. A production controller must wait for every node's observed
  configuration and endpoint behavior before reporting positive active
  weight.

## Limits and next gate

The fixture used one Traefik process, one Consul agent, loopback HTTP
backends, and a local API router. Its old/new Consul services were registered
independently; this exposed a gap in Norn's then-stable Nomad job identity.
The opt-in managed translator now gives revisions separate job IDs, but its
private worker and Nomad delivery path have not yet been connected or tested
with two simultaneous jobs. The fixture did not exercise Fleet's pinned Traefik
binary, separate ingress hosts, privileged file publication, authentication
or transport security for the API, a public load balancer, TLS certificate
behavior, rollout interruption, or rollback. The observer compares effective
route fields to the desired document and reports the **matched desired**
SHA-256; Traefik does not return the file's SHA-256 in `/api/rawdata`.

The next M4 implementation gate is a versioned, atomic publisher to every
ingress node with a protected management readback path, followed by per-node
and public endpoint probes and rollback. Only that evidence can support
`ActiveWeight` and terminal deployment success.

## Local publication and readback

`ObserveWithdrawnRoute` now requires every named node's effective Traefik
configuration to lack the managed public-host router after withdrawal. It
accepts the generation-preserving `.invalid` tombstone and rejects a stale
node or another enabled router claiming the public host. Local two-node HTTP
fixtures cover those cases. Both active and withdrawn observers fail closed
when another enabled router uses `HostRegexp`, because its overlap with the
managed hostname has not been proved away. This is only effective-config
observation: exact
file-generation readback and public-host 404 probes are still separate
requirements, and no Fleet node was observed through this function.

`ProbeRenderedRouteNodes` now sends the intended public-host request through
each explicitly named ingress IP:port. It keeps the Host header and TLS server
name, verifies normal TLS trust and hostname rules, disables redirects and
proxies, bounds time and response size, and compares a caller-supplied exact
response digest. Two-node local HTTP fixtures reject a stale second response
and duplicate node addresses; a TLS fixture rejects a certificate for the
wrong public hostname. It does not enumerate Fleet nodes or prove the public
load-balancer path, backend traffic percentage, or app-specific probe
identity. Those checks remain required before positive `ActiveWeight`.
`ProbeRenderedRoutePublic` separately resolves the public hostname through
normal DNS, verifies TLS when applicable, disallows redirects/proxies, and
checks the same bounded expected response. A local dial fixture covers its
request and rejection behavior, but no actual public Fleet path has been
probed. One public response cannot establish load-balancer distribution.

## Combined local proxy rehearsal after probe additions

On 2026-09-27, the disposable Traefik 3.7.13 and Consul 2.0.4 fixture was
rerun against the current ingress package. Both revision backends returned
the same exact `orders-ready-v3` body at `/readyz`, while their `/` responses
remained distinct. One local Traefik process accepted the generation-1
published route; exact file readback and effective `/api/rawdata` observation
matched the desired route. `ProbeRenderedRouteNodes` then passed through
Traefik at `127.0.0.1:18080` with `Host: orders.example.test` and the
expected readiness-body SHA-256
`bc818941dca083541d792be73c4fe1ae1ac2eedd04267a6b73f26b9db00c885d`.
One hundred root requests returned 70 old and 30 new. Generation-2
withdrawal passed file readback and `ObserveWithdrawnRoute`; the original
public-host request returned 404. The shell rehearsal exited zero and all
five fixture listeners were gone afterward.

This combines the local publisher, effective-route observer, per-node probe,
and withdrawal check in one real proxy process. It still uses one loopback
ingress node and unprotected local HTTP. It does not run
`ProbeRenderedRoutePublic` through real DNS or a public load balancer, test
TLS on Fleet, prove simultaneous multi-node propagation, or connect a durable
controller intent to `ActiveWeight`.

`ingress.PublishRenderedRoute` writes one canonical weighted route into a
trusted file-provider directory under a cross-process lock. It fsyncs a
private temporary file, atomically renames it, and syncs the directory.
`ReadPublishedRouteRevision` returns the exact file content SHA-256 and its
generation; a missing file is generation zero. Symlinks and oversized files
are refused. A sync error after rename is indeterminate and requires file
and Traefik readback before retry. These are per-node primitives; no Fleet
node agent invokes them yet.

## Publisher-to-runtime rehearsal at `bc311d1d`

On 2026-09-27, a second disposable loopback run replaced the earlier direct
file write with `PublishRenderedRoute` and `ReadPublishedRouteRevision`. It
started local Consul 2.0.4, Traefik 3.7.13, and two HTTP backends in a fresh
watched directory. Exact file readback matched the rendered SHA-256
`9cdb695b409edfd97b74136fa69140e3fa20a04caf927ce13f7185ae7f778e7a`.
`ObserveRenderedRoute` accepted Traefik's live `/api/rawdata` response for the
same revision, and 100 public-host requests through Traefik returned 70 old
and 30 new. The shell rehearsal exited zero, and a post-run listener check
found no processes on the five fixture ports. The fixture used one loopback
Traefik, one Consul, local HTTP, and an unprotected loopback management route;
it still does not establish multi-node propagation, protected management
access, public TLS/LB behavior, or rollback.

## Generation-fenced rollback and runtime correction

The initial hash-only `RemovePublishedRoute` experiment was replaced after an
A→B→A review showed that a repeated content hash would let a stale writer
match again. The current file contains a monotonic generation comment in the
same atomic file as the Traefik route. `PublishRenderedRoute` requires a
matching prior per-node revision and a higher generation; an exact repeated
generation/content is idempotent after an ambiguous response. Race tests
cover competing writers, A→B→A, tampering, symlinks, and stale-generation
refusal. The generation must be issued and authenticated by a future durable
Fleet route intent; a caller cannot invent a high number and claim authority.

A first attempt to retain an absent-state generation using an empty HTTP
document failed a live Traefik watcher test: the old public router stayed
loaded. `WithdrawPublishedRoute` now atomically replaces it with an explicit
inert router for a reserved `.invalid` host. The same disposable Consul 2.0.4
and Traefik 3.7.13 fixture passed with the generation-1 route: exact file
readback, enabled effective route, and 70 old / 30 new responses over 100
requests. Withdrawal at generation 2 removed the original public-host rule
from `/api/rawdata`, and the original host returned HTTP 404. The rehearsal
exited zero and cleaned up its loopback processes. This establishes only a
one-node local apply/withdraw path. A Fleet controller still needs durable
route intent, authorized generation delivery, every-node publication and
protected readback, endpoint probes, rollback coordination, and failure
recovery before `ActiveWeight` can be positive.
