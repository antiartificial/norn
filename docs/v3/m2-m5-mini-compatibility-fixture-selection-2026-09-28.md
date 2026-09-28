# Mini database compatibility fixture selection — 2026-09-28

Status: proposed private rehearsal scope; no live mutation or release-gate sign-off.

An authenticated, read-only Mini inventory on 2026-09-28 returned 27 app
records, 44 service records, zero active control operations, 13 active
incidents, and no configured Fleet node pools. Seven app specs declared the
legacy `infrastructure.postgres` database. Four were healthy with running
allocations: `field-harbor`, `mail-indexer`, `signal-sideband`, and
`turnkey-offer-intake`. The other three (`gitea`, `hello-norn`, and
`motifgarden`) had no current allocation. This is a point-in-time inventory,
not a health guarantee or proof of application database contents.
An independent read-only query of Mini's local Postgres.app `pg_database`
listed both proposed fixture database names, `mailindexer` and
`turnkey_offer_intake`. Existence alone does not prove that the current jobs
connect to those databases or that the data can be restored.

A second read-only `pg_stat_activity` query found idle sessions using role
`norn` on `mailindexer` and role `turnkey_offer_intake_app` on
`turnkey_offer_intake` (three and one sessions respectively at collection
time). It also found role `norn` on `field_harbor` and `signal_sideband`.
Session presence is not attribution to a particular Nomad allocation, but it
establishes that the Mini's legacy application databases currently have
different PostgreSQL login roles. The v3 `legacyPostgres` profile has only one
`role` and `credentialRef` for every legacy database declaration. Therefore
one default catalog entry cannot represent both proposed fixtures' observed
login identities. The example `legacy_apps` role is illustrative and must not
be applied to Mini as if it reflected its live connection identity.
The same read-only query found active `norn` sessions on control database
`norn_v2`; `pg_database` lists `norn` as owner of both `norn_v2` and
`mailindexer`. V3 catalog purpose separation deliberately rejects an
application binding that shares a control role on the same provider. A
per-database map alone therefore cannot admit `mail-indexer` unchanged under
the intended security boundary. That app and the other observed `norn`-role
app databases need a reviewed role/credential separation and compatible job
connection transition before catalog activation. No role, privilege, secret
or job was changed during this inspection.

The read-only follow-up checked all seven declared legacy databases against
`pg_database` ownership and active `pg_stat_activity` roles:

| Database | Database owner | Session role observed |
| --- | --- | --- |
| `field_harbor` | `0xadb` | `norn` |
| `gitea` | `0xadb` | None at collection time |
| `hellonorn_db` | `0xadb` | None at collection time |
| `mailindexer` | `norn` | `norn` |
| `motifgarden` | `norn` | None at collection time |
| `signal_sideband` | `0xadb` | `norn` |
| `turnkey_offer_intake` | `turnkey_offer_intake_app` | `turnkey_offer_intake_app` |

This narrows the currently observed login transition to at least
`field-harbor`, `mail-indexer` and `signal-sideband`. `motifgarden` also needs
an ownership/credential decision before its inactive workload can be treated
as preserved. An absent session for `gitea` or `hello-norn` gives no evidence
of which role those jobs would use. The private inventory must inspect their
configured connection references and database privileges; the table alone
does not authorize role changes.

A further read-only Mini check on 2026-09-28 compared current PostgreSQL
sessions with registered Nomad service jobs. Active `norn` sessions also
appeared on `like_trove` and `vigil_gateway`; those databases are owned by
`norn` but were outside the seven `infrastructure.postgres` declarations in
the earlier app-spec inventory. Nomad job inspection (printing only parsed
database/user identities and environment key names) found:

| Job/task | Declared database connection identity | Consequence |
| --- | --- | --- |
| `like-trove` web | `DATABASE_URL`: `norn` / `like_trove` | Inventory as a direct-URL database consumer outside the seven legacy declarations. |
| `mail-indexer` web | `POSTGRES_USER` / `POSTGRES_DB`: `norn` / `mailindexer` | Role split must preserve this job's connection path. |
| `mail-mcp` mcp | `DATABASE_URL`: `norn` / `mailindexer` | The same database has a second registered consumer; split/drain both jobs before old-role access is removed. |
| `signal-sideband` web | `DATABASE_URL` and `DB_*`: `norn` / `signal_sideband` | URL takes precedence in checked-out app code; a `DB_USER` edit alone does not switch the job. |
| `vigil-gateway` web | `DATABASE_URL`: `norn` / `vigil_gateway` | Inventory as another direct-URL consumer outside the seven legacy declarations. |
| `turnkey-offer-intake` web and worker | `DATABASE_URL`: `turnkey_offer_intake_app` / `turnkey_offer_intake` | Both processes require the same binding readback. |
| `watchtower` web | `DATABASE_URL`: `watchtower` / `watchtower` | Distinct observed role; account for it in the broader Mini database inventory. |

`field-harbor` had an active `norn` database session but no matching DB
environment keys in its inspected web task. `contextdb` likewise had a
`hermes` session without matching keys in its inspected tasks. Their
connection source remains unresolved. A PostgreSQL session is not by itself
attribution to a particular Nomad allocation, and job declarations do not
prove the currently running process uses each value. Before activating a
catalog or fencing an old role, join the exact allocations, process connection
sources and all writers, including jobs outside the legacy InfraSpec syntax.

The current `mail-mcp` checkout was inspected read-only. Its configuration
loads `DATABASE_URL`; `NewStore` constructs a PostgreSQL pool, startup calls
`InitSchema` (a `CREATE TABLE IF NOT EXISTS` statement), and the MCP tool path
records a message interaction with an `INSERT`. The store also contains a
voice-feedback upsert. This is a write-capable code path, not merely a
read-only lookup client. The registered Nomad job is running with image tag
`mail-mcp:7a5dd2bb8c77-dirty`; the local checkout is dirty too, so the
source cannot establish the exact deployed image contents. Treat `mail-mcp`
as a possible live writer and startup DDL consumer until exact-image/runtime
readback proves otherwise. A `mailindexer` role split must include its
connection update, startup behavior, in-flight request drain, and old-role
session/access checks; the single-job private fixture does not cover these.

For the proposed first fixture, a read-only `mailindexer` ownership query
found eight ordinary tables, four sequences and 31 indexes owned by `norn`.
The database itself is also owned by `norn`; the `public` schema uses
`pg_database_owner`. No `norn`-owned application functions or extensions were
observed. This makes a password-only swap insufficient: a replacement app
role must be rehearsed with its database, table, sequence and migration DDL
privileges, then verified from the actual app job and its migration command.
Do not use a broad `REASSIGN OWNED BY norn` against the live server as a
shortcut: the role also owns control database `norn_v2`. The private
rehearsal must scope ownership changes to the selected application database
and verify that `norn_v2` ownership and control sessions are unchanged.
The opt-in [disposable role-split fixture](../../v2/scripts/test-mini-role-split-disposable.sh)
passed locally with Homebrew PostgreSQL 16.15. It created synthetic control
and app databases owned by `norn`, transferred only the app database, table
and sequence to a separate role, preserved two data rows and migration DDL,
blocked the old role from connecting to the app database, and kept control
ownership/access intact. A follow-up run returned the synthetic database,
tables and sequence to the prior role after the new role had written, kept all
three rows, removed the replacement role's connection access and again
preserved `norn_v2` ownership. It is a procedure-shape check only: it does
not cover `mailindexer`'s actual schema, grants, secrets, running job,
connection drain, operational rollback or production-key backup.

The [private `signal_sideband` copy rehearsal](../../v2/scripts/mini-signal-sideband-private-role-copy-rehearsal)
then passed on Mini with Postgres.app 17.7 (script SHA-256
`4094790fad9ad968c5fbf4bbed6807d5b1fb25f365c95c104900e15f172de31d`).
It made a read-only dump over Mini's local socket, restored it into a
disposable socket-only cluster, and kept all data on Mini. The restored copy
had 9,223 `messages` rows; all 11 application tables were transferred from
the copied `norn` owner to a separate `signal_app` role. That role read the
rows and created a migration-probe table. The old role could no longer
connect to the copied app database, while the copied control database stayed
owned by and accessible to `norn`. The app database retained its actual
`0xadb` owner shape. Scratch and the transferred script were removed; a
read-only follow-up confirmed live `norn_v2` and `signal_sideband` database
owners unchanged. This is stronger than the synthetic fixture, but it still
does not exercise the live Nomad job, secret rotation, source connection
drain, or all application behavior. A second private-copy run rehearsed
rollback after a new-role write: all 12 copied tables returned to the old
owner, the 9,223 `messages` rows and probe write remained, the replacement
role lost copied-database access and the copied control database remained
unchanged. This is not a protected live role rollback.

During a further private-copy run, the opt-in `TestReadinessWithDatabase` from
signal-sideband draft PR #1 connected as `signal_app` while that role held the
copied application objects and passed in 0.01 seconds. The same run completed
the role split and rollback checks. This proves the proposed `/ready` handler
can reach the isolated copied database with the distinct app role; it does
not prove the live Nomad task has switched credentials or that the protected
database remains available through a cutover.

The live Nomad `signal-sideband` web task currently declares `DB_USER=norn`
and a `DATABASE_URL` whose parsed username is also `norn` and database is
`signal_sideband`; the URL password and other secret values were not printed.
The app's checked-out `main.go` gives `DATABASE_URL` precedence over the
individual `DB_*` values. That checkout has uncommitted `infraspec.yaml` and
`secrets.enc.yaml` edits, so it must be preserved and compared to the exact
deployed source before any app change. Its startup code logs a database
store-construction failure and continues in memory-only mode; in that case
it does not start the HTTP server. For a parseable but unreachable URL,
`pgxpool.NewWithConfig` creates a pool without a synchronous `Ping`, so the
server can start and its `/health` route returns 200 while database work
fails. Therefore a green allocation or `/health` response cannot prove a
successful live credential switch. Draft app
[PR #1](https://github.com/antiartificial/signal-sideband/pull/1) adds a
bounded database-backed `/ready` endpoint in an isolated worktree; it is
neither merged nor wired into the deployed InfraSpec. A protected transition
needs that readiness path plus an independent database-backed read/write
probe and confirmation that the deployed job uses the new URL identity;
changing only `DB_USER` or `DB_PASSWORD` is insufficient.

Read-only Nomad and Docker inspection tied running allocation
`a8878127-ae14-c6c0-cb9d-b9266a727747` to image tag
`signal-sideband:31701b217777-dirty` and exact local image ID
`sha256:d8f6678a3d79ad7c1e0b44363484607dccd291161d738221f74d2d0233c2d660`.
The Mini checkout is at commit `31701b21777798c2b1e42881a643ec7ed09485af`
with uncommitted InfraSpec and encrypted-secret edits. The dirty image tag
does not identify the source diff used to build that running image. Before a
live job or credential change, retain the exact running image and job
definition for rollback and establish a reviewed, reproducible app release
from an identified source tree. A Git SHA prefix alone is insufficient
provenance for this allocation; no image or job was changed in this review.
The exact running image was exported by its current tag to an owner-only
local task artifact at
`/Users/arti/Documents/Codex/2026-09-26/i-d/work/signal-sideband-running-image-tagged.tar.gz`
(58,932,706 bytes; SHA-256
`d221d26bab9699a99116787f5a1637a848d10e31c36a8d6df44759625fe06727`).
The archive passed gzip and manifest checks, retained the current tag, and
loaded in a separate local Docker daemon as the same image ID shown above,
with the same six root-filesystem layer digests. The test-loaded image was
removed afterward. This is a local rollback input, not a published signed
release, source provenance proof or substitute for a verified job rollback.

Use `mail-indexer` as the first **unchanged legacy PostgreSQL** fixture: its
declared web process, database and endpoint are present, and the inventory
reported one healthy allocation. It is a smaller compatibility case than an
app with periodic work or a host volume. Add `turnkey-offer-intake` to cover
the web-plus-worker shape: its spec declares one legacy PostgreSQL database,
one endpoint and both processes; the inventory reported two healthy
allocations. Snapshot-retention warnings currently affect that app, so its
backup/restore state must be reviewed before any rehearsal that relies on
its snapshots. These are proposed fixtures, not permission to run an upgrade.

The private-copy schema-47 rehearsal preserved all 28 original control tables
and 261,547 rows and passed passive startup. It did **not** compare these two
apps' actual database connection identity, job IDs, allocation behavior,
endpoint routing or application data after candidate promotion. The resolver
tests prove the legacy mapping contract on disposable PostgreSQL, not this
Mini's application path.

Draft PR #77 now has `legacyPostgresBindings`: an unchanged database name
selects an explicit application binding with its own role, credential and
generation. Its resolver tests cover distinct roles, missing entries,
misbound names, control-role refusal and transition refusal. This is code
capability, not a populated or activated Mini catalog. Before activation,
populate the map from a private credential and job-connection inventory,
omit the catch-all default so missing entries fail closed, and recredential
the `norn`-role application connections
through an independently rehearsed, reversible transition that preserves
running workloads and data. Keep each legacy InfraSpec valid without a source
edit. Rehearse the final distinct roles against private database copies
before any protected runtime activation.

For the private M2/M5 rehearsal, capture an owner-only before manifest for
each fixture: exact app record and accepted spec fingerprint; declared
database name and credential reference identity without secret values;
database service/role identity; Nomad base job ID, modify index and allocation
IDs; endpoint-to-ingress destination; and application health/data probes.
After passive candidate startup and isolated promotion/rollback, require the
same logical database and route, preserved job identity unless a reviewed
mapping says otherwise, no duplicated worker work, and matching application
probes. Keep the full source records private and attach only redacted
fingerprints and pass/fail receipts to the release review.

The other five legacy PostgreSQL declarations and all non-PostgreSQL apps
remain in the complete Mini preservation inventory. Passing these two
fixtures cannot sign the whole Mini upgrade gate; inactive jobs, periodic
work, volumes, duplicate `watchtower` records and unmatched ingress routes
still need explicit disposition under the M0/M5 ledger.
