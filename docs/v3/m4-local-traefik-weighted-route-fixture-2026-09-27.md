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
backends, and a local API router. It did not exercise Fleet's pinned Traefik
binary, separate ingress hosts, privileged file publication, authentication
or transport security for the API, a public load balancer, TLS certificate
behavior, rollout interruption, or rollback. The observer compares effective
route fields to the desired document and reports the **matched desired**
SHA-256; Traefik does not return the file's SHA-256 in `/api/rawdata`.

The next M4 implementation gate is a versioned, atomic publisher to every
ingress node with a protected management readback path, followed by per-node
and public endpoint probes and rollback. Only that evidence can support
`ActiveWeight` and terminal deployment success.
