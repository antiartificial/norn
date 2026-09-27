# Mini M0 read-only baseline refresh — 2026-09-24

This is a point-in-time read-only checkpoint collected at 2026-09-24 22:29
UTC. It updates the earlier M0 inventory and measurements; it is not an
upgrade fixture, a release approval, or M0 sign-off. No Mini API, database,
job, route, secret, or provider state was changed.

## Collection boundary

The standard authenticated inventory ran against Mini's loopback API, with its
token decrypted only in the remote process. The raw response files remained in
an owner-only temporary directory and are not part of this repository. A
second SSH command made aggregate-only PostgreSQL catalog, size, and connection
queries. It did not read application rows, secret values, application
definitions, or endpoint URLs.

```sh
bash /Users/arti/.codex/skills/norn-platform/scripts/norn_inventory.sh \
  --remote mini --api http://localhost:8800 \
  --remote-sops-env /Users/0xadb/.config/norn/api.env.enc.json
```

## Observed Mini control plane

| Surface | Observation |
| --- | --- |
| Current API release | `v2.20.0-platform-30-ga5da8ef`; API release record source `a5da8ef15d12e9eca7561e90b90d96f6dc652a21` |
| Installed API binary | `/Users/0xadb/go/bin/norn-api`; SHA-256 `2fdc974ec8b7234c9f73ec9f2156abf1eee06bd63e3d2853572c6f93f79e6e31`; 38,027,650 bytes |
| Supervision | `com.norn.api` running, PID 93746; its LaunchAgent points to the private launcher rather than exposing runtime inputs |
| Control PostgreSQL | Postgres.app 17.7; 28 `public` base tables; neither `schema_migrations` nor `control_schema_migrations` exists |
| Database bytes | 241,626,259 total; 232,611,840 user-table total; 144,793,600 heap; 86,753,280 indexes |
| Database connections | 1 active aggregate measurement query and 3 idle connections |
| Largest current tables | `beacon_events` 109,625,344 bytes; `control_events` 79,716,352; `mutation_audit_events` 30,416,896; `saga_events` 9,076,736 |
| Runtime inventory | 27 app records, 22 declared for deployment, 44 manifest processes, 26 endpoint records, and 22 reported allocations |
| Control state | 0 active operations; 13 active incidents; host status `ok`; production readiness `blocked` |
| Fleet | `fleet_configured=false`, with zero configured node pools |
| Retention signal | Snapshot retention remains over limit for two apps |

The 241,626,259-byte database sample is 884,736 bytes above the first
15:59 UTC sample in [the prior measurement](m0-mini-measurements-2026-09-24.md).
This same-day observation is not sufficient to establish a growth, retention,
or restore-time budget.

## Candidate source and schema contract

This refresh was prepared from v3 integration commit
`f9aad413e430addf4f14617e45bde8e3c52beca0`. Its immutable catalog has
**17** migrations and its compiled compatibility contract is reader version
**3**, writer version **14**. Migration 17,
`archive-backed-operation-acceptance-retirement`, raises both floors from the
previous catalog because old readers resolve replays from the hot acceptance
row. The current Mini has no migration ledger, so it cannot be claimed to
meet any candidate schema contract without the guarded adoption/rehearsal
path.

This supersedes the candidate-version portion of the earlier
[source-to-schema manifest](m0-mini-source-schema-manifest-2026-09-24.md),
which accurately described its then-current 16-migration candidate but is not
the catalog at `f9aad41`.

## Ownership evidence: what this refresh does and does not prove

The `/api/services/manifest` result is derived by the running API from its
discovered app specifications and then enriched with Consul health. It is a
useful declared app/process-to-endpoint view, but it is not a direct Nomad job
or allocation inventory. The `/api/cloudflared/ingress` result reads hostname
entries from cloudflared configuration; it does not establish which app owns a
hostname or whether traffic reaches its listener. The aggregate PostgreSQL
queries identify the control database only; they do not resolve application
database targets, roles, or connection ownership.

The following M0 joins therefore remain unproven and must be collected as
separate, sanitized evidence before a representative fixture or upgrade
rehearsal is selected:

1. Source spec to actual Nomad job and allocation, including inactive and
   duplicate-record cases.
2. Declared endpoint to cloudflared rule, Traefik/Consul service, listener,
   and accountable route owner.
3. App process to volume, secret reference, database target/role, backup path,
   and accountable owner.
4. A reproducible sanitized fixture and an API-level upgrade/rollback
   rehearsal using the 17-migration, reader-3/writer-14 contract.

The previous isolated data restore remains useful evidence for migrations
1–16 only. It did not exercise an API binary, old-reader drain, route/job
ownership, application databases, or rollback after a writer-14 migration.
No M0–M3 milestone is signed off by this refresh.

A later [read-only app-to-Nomad and hostname join](m0-mini-workload-join-2026-09-25.md)
matched the API's live allocation references to Nomad and identified the
duplicate app, inactive jobs, unmatched hostnames, and volume/database
references requiring owner review. It does not prove route or data ownership.
