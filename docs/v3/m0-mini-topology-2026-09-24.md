# Mini M0 topology comparison — 2026-09-24

Read-only Norn API inventory at 2026-09-24 16:28 UTC, using the authenticated Mini inventory script. This compares the API's app records, service manifest v2, and cloudflared ingress hostname list. It is a source for fixture selection and owner review, not proof of Nomad jobs, route reachability, volume ownership, or database ownership.

| Surface | Observation |
| --- | ---: |
| App records | 27 records, 26 distinct names; 22 records declare `deploy: true`, 5 `deploy: false` |
| Service manifest | 44 process records; 26 endpoint records |
| Manifest process exposure | 11 public, 6 private, 1 local, 26 internal |
| Manifest process status | 20 passing, 24 unknown; many unknown records are cron/worker processes and need type-specific interpretation |
| Runtime allocations reported by app records | 22 allocations across 18 records reporting `running` |
| Cloudflared ingress | 16 hostname entries, 15 distinct hostnames |
| Endpoint/ingress hostname comparison | 16 distinct manifest endpoint hosts, 9 also in ingress; 6 ingress hosts not in the manifest endpoint set |
| Control activity | 0 active operations, 13 active incidents; production readiness `blocked` |

The five `deploy: false` apps (`audio-scene-lab`, `gitea`, `motifgarden`, `norn-bloom`, `sync-in`) have no service-manifest processes. Four `deploy: true` app records (`ad-asset-verifier`, `ft-trove`, `hello-norn`, `its-alive-api`) reported no allocation; `its-alive-api` reported Nomad status `dead`, while the other three had no status. An owner must distinguish intentional suspension from failed desired work before using these records in an upgrade rehearsal.

The API returned two `watchtower` records. Their compared JSON differs at `spec.processes.web.command`; both reported a running allocation, and the manifest contains two `watchtower-web` records. This duplicate needs an owner and source-path explanation before it becomes a representative fixture or a target for migration. The command values and app definitions were not copied here.

Endpoint/ingress set differences are not automatically routing defects: private and tailnet endpoints may deliberately lack a cloudflared hostname, and ingress can serve non-Norn routes. A route owner must classify each unmatched hostname using live cloudflared, Consul, and listener evidence. The API also warned that snapshot retention exceeded the configured limit for `field-harbor` and `turnkey-offer-intake`.

## Still required for a deployable M0 fixture

1. Resolve each app record to its source spec, actual Nomad job/allocation, endpoint/route owner, volume mount, secret reference, and database target. The API inventory alone cannot prove these joins.
2. Select representative running, inactive, duplicate, cron/worker, database, and public/private-route cases with named owners; sanitize them for CI.
3. Record a private, isolated restore of the exact Mini database with workers, webhooks, cron, outbound network, and provider mutation disabled. Compare schema and rollback behavior against the signed API release.
4. Repeat byte and connection measurements over an operationally useful interval before setting evidence/retention/restore budgets.

The raw inventory stayed in a private local temporary directory with mode `0700`; no raw API payload, secret value, endpoint URL, or application definition was committed.

## Read-only Nomad job join — 2026-09-24 23:10 UTC

A separate read-only query on the Mini compared `spec.name` from the authenticated
`/api/apps` response with `Summary.JobID` from local `nomad job status -json`.
Only aggregate results and unresolved names were emitted; no job specification,
app definition, token, or credential was exported. This is an exact ID join,
not proof that a job is healthy or belongs to the intended app record.

| Join | Observation |
| --- | ---: |
| API app records / distinct names | 27 / 26 |
| Nomad status rows in that query | 292 |
| App records / distinct names with an exact Nomad job ID | 19 / 18 |
| Exact-match jobs with an allocation entry | 17 of 18 |

The eight distinct names without an exact job ID were `ad-asset-verifier`,
`audio-scene-lab`, `ft-trove`, `gitea`, `hello-norn`, `motifgarden`,
`norn-bloom`, and `sync-in`. Three of these (`ad-asset-verifier`, `ft-trove`,
`hello-norn`) declare `deploy: true`; the other five declare `deploy: false`.
`its-alive-api` had an exact job ID but no allocation entry. The two
`watchtower` app records both matched the same job ID, so this does not resolve
their duplicate source ownership. Nomad's total row count can change as
periodic children appear; the join and counts above are one point-in-time
sample.

The next owner review must resolve these unmatched and duplicate records, then
join the exact jobs to allocations, volumes, routes, and database targets before
selecting a representative upgrade fixture.
