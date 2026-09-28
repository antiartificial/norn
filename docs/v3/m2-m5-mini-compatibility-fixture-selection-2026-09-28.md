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
