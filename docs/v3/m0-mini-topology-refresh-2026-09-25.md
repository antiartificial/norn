# Mini M0 topology refresh — 2026-09-25

Read-only point-in-time evidence at approximately 06:28 UTC. The authenticated
Mini inventory, local Nomad API, cloudflared configuration, TCP listeners, and
local PostgreSQL catalog were queried. No job, route, database, API, or secret
state was changed. The private inventory files remained outside this repo.
This is an ownership join for fixture selection, not a route health or release
qualification result.

| Join | Observed result |
| --- | --- |
| Norn app records | 27 records, 26 distinct names; `watchtower` remains duplicated |
| Exact app name to Nomad job ID | 18 distinct matches; 17 jobs have running allocations |
| No exact job | `ad-asset-verifier`, `audio-scene-lab`, `ft-trove`, `gitea`, `hello-norn`, `motifgarden`, `norn-bloom`, `sync-in` |
| Dead exact job | `its-alive-api`, with no allocation |
| Running allocations on exact jobs | 20 (including two each for `contextdb`, `norn-cadvisor`, and `turnkey-offer-intake`) |
| Exact jobs with group volume declarations and task mounts | Five: `field-harbor`, `norn-cadvisor`, `norn-prometheus`, `signal-cli`, `signal-sideband`; six volume declarations and six mounts total |
| Declared InfraSpec PostgreSQL database in local Mini catalog | Seven of seven: `field-harbor`, `gitea`, `hello-norn`, `mail-indexer`, `motifgarden`, `signal-sideband`, `turnkey-offer-intake` |
| Cloudflared hostname rules | 16 rules, 15 distinct hostnames; nine distinct hostnames match a manifest endpoint host |
| Cloudflared TCP targets | 12 of 15 distinct target rules accepted a TCP connection in this sample; three refused it |

Nomad reported 289 job rows, many of which are periodic children; the join
above uses only exact app-name IDs. The `watchtower` duplicates both map to
one job and one running allocation, so they do not establish two independent
sources or owners. The five volume-bearing jobs are evidence of configured
mounts, not of mount contents, backup coverage, or source-path ownership.

Of the nine matching endpoint/ingress hostnames, the corresponding manifest
apps are `context-kitchen-sink`, `its-alive-api`, `like-trove`, `mail-agent`,
`mail-indexer`, `signal-sideband`, `ticketsite`, `turnkey-offer-intake`, and
`watchtower`. The ingress TCP target associated with `its-alive-api` refused
connections, consistent with its dead Nomad job. Two other ingress targets
without an exact manifest endpoint hostname also refused connections. A TCP
connection only proves a listener accepted a socket at that instant; it does
not prove HTTP routing, TLS, Consul registration, or an app allocation behind
the listener. The six unmatched ingress hostnames need route-owner
classification; they may be outside Norn.

The seven declared PostgreSQL database names were compared to the 21
connectable names in the Mini local catalog by digest inside the collection
script; all seven existed. This does not prove application connection target,
role privileges, data ownership, backup/restore coverage, or that apps without
an `infrastructure.postgres` declaration do not use a database. The previous
job-spec scan found database-looking environment **key names** in nine jobs,
including `watchtower` and `vigil-gateway`, which have no declared InfraSpec
PostgreSQL database. Values and connection strings were not inspected or
recorded.

## Watchtower source ownership follow-up

A read-only 08:23 UTC inventory still returned two `watchtower` records. The
two source directories are the current `watchtower` checkout and a retained
`watchtower.pre-git-20260916165652` checkout. The latest deployed Watchtower
record has source commit `8ea1c575859f3ac507d1e95b5eed76b0ae4bf5f8`,
matching the current checkout's head. The retained checkout's head is
`ade8d26`; it also has uncommitted work, so it must not be moved or rewritten
as an incidental cleanup. This identifies the current checkout as the
deployed source at this sample, while preserving the retained checkout for
owner review.

The candidate discovery code now recognizes a regular
`.norn-discovery-ignore` marker in a source directory. The marker is an
explicit way to exclude retained source from both deployable and all-app
inventory. No marker was written on Mini and the installed binary does not
yet implement it. Before v3 promotion, the source owner must approve the
retained checkout's exclusion, place the marker without changing its other
work, and verify that the candidate inventory has one Watchtower record.

## Owner decisions needed for a representative fixture

1. Confirm the current `watchtower` checkout as the intended source owner,
   explicitly exclude the retained pre-git checkout, and verify one candidate
   inventory record before selecting a fixture.
2. Classify the eight app names with no exact job and the dead `its-alive-api`
   job as intentionally inactive or failed desired state. Its ingress listener
   is currently closed.
3. Assign route owners to the six unmatched ingress hostnames and three closed
   targets; compare each intended route through cloudflared, Traefik/Consul,
   actual listener, and allocation before using it in an upgrade rehearsal.
4. Assign storage owners to the five volume-bearing jobs, then verify exact
   mount source/target, persistence, snapshot, and restore lineage privately.
5. Assign database owners for the seven declared local database names and the
   apps with database-looking runtime keys but no declaration. Verify effective
   target and role from a private runtime connection test rather than inferring
   them from catalog existence or environment key names.

No endpoint URL, hostname, listener address, database name, mount path,
credential, job specification, or application definition was committed.
