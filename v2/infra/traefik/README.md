# Norn regional ingress

This portable InfraSpec runs one Traefik allocation in every declared region.
Traefik watches the regional Consul catalog and only exposes services carrying
`traefik.enable=true`; Norn emits those tags for processes with endpoints.

The template is deliberately `deploy: false`. Before enabling it:

1. Copy it to the apps directory as `norn-traefik/infraspec.yaml`.
2. Set `regions` and `primaryRegion` to match the application fleet.
3. Set `CONSUL_HTTP_ADDR` to the region-local Consul endpoint. Linux commonly
   uses a routable node or mesh address; Docker Desktop supports
   `host.docker.internal`.
4. In production, replace `build.image` with the approved, signed Traefik OCI
   digest.
5. Set Norn's `NORN_INGRESS_URL` to the stable regional origin, normally
   `http://127.0.0.1:18080`, and then set `deploy: true`.

`hostPort: 18080` is intentionally fixed for ingress only. Application
allocations use dynamic host ports and Consul discovery, allowing multiple
endpoint-backed allocations on the same node.
