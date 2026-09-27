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

## Subsequent local publisher slice

The `ingress.PublishRenderedRoute` primitive now guards one node's watched
directory with a cross-process lock and expected SHA-256 revision. It checks
the canonical single-router YAML, refuses a stale writer or symlink target,
fsyncs a private temporary file, atomically renames it, and syncs the
directory. `ReadPublishedRouteRevision` reads back the exact bounded regular
file revision and refuses symlinks, allowing an indeterminate file write to
be reconciled before retry. Package race tests passed for conflicting concurrent writers,
idempotent replay, tampering and stale revisions. This is a local file apply
primitive only: no Fleet node agent invokes it, and no multi-node publication,
protected rawdata readback, endpoint probe, or rollback was exercised by
these tests. A sync error after rename is an indeterminate apply and must be
resolved by reading the node's actual file and Traefik state before retry.
