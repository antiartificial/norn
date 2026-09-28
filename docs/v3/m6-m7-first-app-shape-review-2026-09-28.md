# First application cutover shape review — 2026-09-28

Status: **read-only Mini inventory, not a selected live application or writer
inventory**. No application job, database, credential, or route was changed.
The deploy-disabled synthetic web/worker mobility fixture remains the first
M6/M7 rehearsal workload.

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
