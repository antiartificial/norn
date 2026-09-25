# Mini workload join — 2026-09-25

Read-only point-in-time collection at approximately 21:36 UTC. The
authenticated loopback Norn inventory was joined locally to Mini's loopback
Nomad jobs and allocations. The inventory response files and reduced Nomad
metadata stayed in an owner-only local temporary directory and were removed
after analysis. No job definitions, credentials, endpoint URLs, or database
values are included here. No API, Nomad, route, or database mutation occurred.

## App and job identity

The API returned 27 app records for 26 distinct names. `watchtower` appeared
twice. Twenty-two records declared deployment; three of those names
(`ad-asset-verifier`, `ft-trove`, `hello-norn`) had no base Nomad job at this
snapshot. Mini's Nomad API returned 37 base jobs: 18 service and 19 periodic
batch parents. It also returned many short-lived periodic children; those were
excluded from the base-job count. Every base job name matched one declared app
name or its declared process name. One service job, `its-alive-api`, was dead;
the other 36 base jobs were reported running, including periodic parents.

The API's app records contained 21 allocation references. Each shortened
allocation ID matched exactly one full Nomad allocation ID, and its Nomad job
matched the named app. The two `watchtower` app records referenced the same
allocation, leaving **20 distinct running allocations** represented by the
API. This proves the current API-to-Nomad allocation join for those live
allocations. It does not make the duplicate app source safe for an automated
upgrade or prove inactive job ownership.

## Route and state references

Cloudflared's API listed 16 hostname entries, 15 distinct. Eleven distinct
hostnames matched exactly one hostname in the declared app endpoint URLs;
four had no declared app endpoint match. This is a hostname correlation only.
The inventory API does not expose the cloudflared destination rule, and this
check did not prove DNS, proxy routing, listener ownership, or successful
traffic for any hostname.
The later [destination join](m0-mini-ingress-destination-join-2026-09-25.md)
identified the current technical destinations as Open WebUI (two), Norn API,
and Vigil. Owner and route-path qualification remain open.

The 37 base Nomad job definitions contained eight jobs with declared volumes
and mounts. Five were service jobs: `field-harbor` (one), `norn-cadvisor`
(two), `norn-prometheus` (one), `signal-cli` (one), and `signal-sideband`
(one). Three periodic `field-harbor` jobs each declared one volume and mount.
The check counted definitions; it did not inspect source paths, persistent
contents, or backup ownership.

Twenty-eight base jobs carried database-related environment **key names**.
Among service jobs these were `field-harbor`, `its-alive-api`, `like-trove`,
`mail-indexer`, `mail-mcp`, `signal-sideband`, `turnkey-offer-intake`,
`vigil-gateway`, and `watchtower`. The remaining nineteen were periodic
`field-harbor` and `like-trove` jobs. No environment values or connection
targets were copied into this document. Key-name presence does not prove a
database is reachable, owned by the app, or covered by backup/restore.

## Required owner decisions before Mini cutover

1. Resolve the duplicate `watchtower` app record and identify the canonical
   source without deleting either record during this read-only stage.
2. Decide whether the three declared-but-absent jobs and the dead
   `its-alive-api` job are intentionally inactive or must be preserved and
   exercised in the upgrade fixture.
3. Assign accountable owners for the four unmatched ingress hostnames, then
   verify each cloudflared destination, Consul/Traefik service, and listener
   through the actual route path.
4. Identify the owner, restore path, and migration boundary for each declared
   volume and database target, using protected records for sensitive values.
5. Select a representative set of service, periodic, route, volume, and app
   database workloads for a private upgrade and rollback rehearsal.

The join narrows the M0 exceptions but does not close M0 or authorize a live
Mini upgrade. The live API was still v2 at collection time.
