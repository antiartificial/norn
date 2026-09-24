# Mini private-data restore rehearsal through migration 17 — 2026-09-24

This isolated M0 fidelity checkpoint applied the current v3 control-schema
catalog to a private copy of the Mini control database. It is evidence for the
data-preservation and migration-idempotence portions of the Mini upgrade path.
It neither authorizes a Mini upgrade nor proves rollback, mixed-version, or
application-runtime compatibility.

## Source identity and isolation

The source was read only. At rehearsal time, the Mini's running
`/Users/0xadb/go/bin/norn-api` SHA-256 remained
`2fdc974ec8b7234c9f73ec9f2156abf1eee06bd63e3d2853572c6f93f79e6e31`.
The signed-source verification recorded in the
[Mini control-store measurement](m0-mini-measurements-2026-09-24.md) binds that
same binary hash to signed release source
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21`
(`v2.20.0-platform-30-ga5da8ef`). The Mini working checkout was separately at
`42a397a3773f1f91572abfe9d17944938fcd9246`; it was not used as a substitute
for the running release identity.

The source was PostgreSQL 17.7 database `norn_v2`, with 28 public tables. A
PostgreSQL 17 custom-format dump used `--no-owner --no-privileges`; it was held
only in an owner-only temporary directory, with mode `0600`, and was not
committed. The dump was restored into a disposable `postgres:17` container
(image digest `sha256:d74eeac9a635390a49bc21bd49fccd973de707e2a53a76ac49b552b8712ec46f`).
Docker inspection showed `network=none` and no published ports. The only
target processes were PostgreSQL and a purpose-built schema-migrator binary;
no Norn API, worker, webhook, cron, provider client, or application runtime
started in the target.

## Result

The candidate source was integration commit
`f9aad413e430addf4f14617e45bde8e3c52beca0`. Its
`store.NewControlSchemaMigrator` adopted the unversioned Mini schema and
applied migrations 1 through 17 in one migration transaction. The resulting
ledger and compatibility row reported current migration 17, minimum reader 3,
and minimum writer 14. The second migration invocation applied zero versions
and left compatibility metadata unchanged.

Before migration, the restored database had 28 public legacy tables and
245,383 rows across those tables. After migration it had 45 public tables,
including the 17-row migration ledger. All 28 legacy table row counts and
primary-key fingerprints matched before and after. The aggregate sanitized
integrity digest was identical for both observations. The retained evidence is
only those counts and digest comparisons; no row values, credentials, dump
contents, or table-level fingerprints are in this repository.

The target container and owner-only temporary directory were removed after
verification. This record does not retain a reusable private fixture.

## What this does not prove

No old API or old reader/writer was started against the migrated target.
Because migration 17 raises the minimum reader to 3 and writer to 14,
old-reader coexistence and rollback after the new writer contract remain
unproven. The rehearsal also did not start a candidate API, execute jobs,
validate auth or sessions, compare Nomad/Consul routes, or exercise app
database behavior. The source database was copied outside the platform upgrade
script, so this is not the M5 end-to-end upgrade and rollback rehearsal.
