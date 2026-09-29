# First application cutover shape review — 2026-09-28

Status: **read-only Mini inventory, not a selected live application or writer
inventory**. No application job, database, credential, or route was changed.
The deploy-disabled synthetic web/worker mobility fixture remains the first
M6/M7 rehearsal workload.

Its checked-in InfraSpec currently declares exactly three processes: `web`
(`serve`), periodic `worker`, and periodic `tick`. A model regression now
requires exactly those three names; the Nomad translation test checks each
mode and file mount. The fixture code gates writes in all three modes with
`WRITE_ENABLED`, while the local transfer test independently fences the
dedicated source PostgreSQL runtime role. At deployment, inventory every
periodic child and old allocation, then inspect the exact runtime role and
all other database sessions. Source code shape is not a live writer census
or an owner attestation that no external writer exists. The fixture stays
deploy-disabled until exact source, target, volume, role and release identities
are reviewed.

The Mini Nomad API listed 284 jobs, including retained periodic children.
For each app below, the read-only inspection used the base job's group/task
names and database environment **key names only**. It did not print URL values
or credentials. Parent/child counts are a snapshot, not proof that no
unlisted external process can write the database.

| App | Observed source job shape | Database observation | First-cutover concern |
| --- | --- | --- | --- |
| `contextdb` | One web task and one review worker; no related periodic children in the listing | Both tasks declare `CONTEXTDB_DSN` with a PostgreSQL URL; a read-only version query from the Mini host did not establish the server major | Source connectivity/version and any worker queue or external writers remain unknown |
| `turnkey-offer-intake` | One web task and one worker; no related periodic children in the listing | Both declare `DATABASE_URL`; read-only `SHOW server_version_num` returned PostgreSQL major 17 from each task's configured source | The worker's acknowledged work, integrations, owner, source binding and fence privileges remain unreviewed |
| `watchtower` | One web task; no related periodic children in the listing | The job declares a PostgreSQL `DATABASE_URL` | App-side collectors and external writers were not inventoried; job shape alone is insufficient |

The live application URLs observed here were not Mini's local PostgreSQL
socket. Mini's PostgreSQL 17.7 host measurement therefore cannot stand in for
an application's source database version. `turnkey-offer-intake` has a
read-only source-major observation of 17; it has **not** been selected for a
cutover or checked for a dedicated runtime role, source provider privileges,
backup/restore support, data size, or owner-approved maintenance window.

Next, complete the synthetic fixture's exact source/target binding, writer
inventory, queue/file consistency group, provider plan and cutover rehearsal.
Only then review a named live app with its owner. For a live candidate, read
back every regional service job, periodic parent and child, function job,
allocation, queue consumer, integration, connection pool and database role.
Unknown writers must block quiescence and promotion. A Nomad task list is
supporting evidence, not an exhaustive fence.

## Stored-spec and regional Nomad readback

The local Mini Norn API returned a stored legacy PostgreSQL declaration for
`turnkey-offer-intake` with `web` and `worker` processes. Its stored spec SHA-256
was `13e92693629c7ed40afb7ffe93d02abefec43ed0e15b508b7a9c0ff95915b193`.
An exact local build of `norn-cutover-writer-inventory` (binary SHA-256
`d141693679a30f303389aaf7730f00a7267fa37575c366dbcf95f25e6d8e06b1`)
queried the Mini loopback Norn and Nomad APIs read-only. It found one `global`
job, two live allocations, and no missing or unexpected app-prefixed jobs in
that regional observation. It returned `externalWritersUnverified=true` and
`promotionReady=false`. The same command refused `contextdb`: its stored spec
has no database declaration, despite the Nomad task's `CONTEXTDB_DSN` key.
The transferred binary was removed after the run.

This does not identify database sessions, external integrations, queue state,
credential holders, or uncataloged writers. It neither selects
`turnkey-offer-intake` nor permits a cutover.
