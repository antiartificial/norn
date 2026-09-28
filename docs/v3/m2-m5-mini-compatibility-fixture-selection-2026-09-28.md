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
