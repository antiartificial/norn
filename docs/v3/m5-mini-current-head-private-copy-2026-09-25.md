# M5 current-head Mini private-copy rehearsal — 2026-09-25

The checked `mini-private-copy-rehearsal` script passed against a fresh
read-only dump of the current Mini control database using a locally built
candidate from Norn PR #76 commit
`21e913785ef97d7e97634c1ede84faedb9ffc2ca`. The arm64 candidate binary
SHA-256 was
`dd3c37e36f8a4df5f4f1457d827420b544b8b3a2e35fbc7ac367c85c41708418`.
Its compiled `norn.startup/v2` contract declared catalog migration 38,
minimum reader 5, and minimum writer 30.

The Mini source dump used `default_transaction_read_only=on`. It was restored
into PostgreSQL 17.7 on a private Unix socket with TCP disabled. The first
copy had 28 original public tables and 249,181 rows. The candidate ran
migrate-only twice, leaving a contiguous 1–38 ledger and compatibility floor
5/30. Counts and ordered primary-key fingerprints of every original table
matched before and after migration. The candidate then passed passive/check
health and schema responses with operation recovery, worker, and Nomad watcher
disabled.

The rehearsal was strengthened to compare every **original column** in
primary-key order. A first attempt hashed newly added migration columns too,
which produced false differences. The corrected check records the pre-migration
column projection for each table and hashes that same projection afterward.
A later fresh dump contained 249,197 rows; all 28 table counts, primary-key
fingerprints, and original-column full-row fingerprints matched after two
migrate-only passes. The two row totals are separate point-in-time source
copies and are not compared with each other. Passive/check startup passed
again at migration 38 and compatibility 5/30.

After the rehearsal, its script stopped the private API and PostgreSQL,
removed its scratch directory, and reported verified cleanup. A separate
remote inspection found no matching scratch directory. The transferred
candidate and script were removed from Mini. The live Mini API still reported
`v2.20.0-platform-30-ga5da8ef`; health showed Consul, Nomad, PostgreSQL,
S3, and SOPS up. The live control database, API, workers, and workloads were
not migrated or restarted.

This is current-branch copied-data compatibility evidence, not a signed
release, protected production-backup restore, legacy service fence, rollback,
or live upgrade qualification. Newly added migration columns were not part of
the original-column data comparison. M5 remains
open. The local build copy under `/tmp/norn-v3-m5-current-20260925` remains
because automatic command review rejected a `rm -f` cleanup command; no
alternate deletion method was attempted.

## Fresh socket-only source rehearsal

At approximately 2026-09-25 21:00 UTC, the rehearsal ran again with candidate
source `cd4152b2e2b333c03cf9d4f24e564bb74c4f863a`. The disposable
Darwin/arm64 binary SHA-256 was
`47f7c6f76b6edeed5e7985d2d4cb1fa9c724dba30d5e8a8d19dcc2caf9790ef7`.
The source dump connected to Mini's PostgreSQL 17.7 through its owner-local
Unix socket using a read-only transaction setting, without a database
password. The updated rehearsal script accepts the socket directory in the
PostgreSQL URL's `host` query parameter. It restored the dump into a private
socket-only PostgreSQL cluster and found **28 original public tables and
249,771 rows**. Migration 1–38 and the second migrate-only pass succeeded;
all original-table counts, primary-key digests, and original-column full-row
digests matched. Passive/check API health and schema checks passed with
minimum reader 5 and writer 30.

The script removed its private database, dump, and logs. Exact temporary
candidate/script directories on both Macs were removed and verified absent.
The live Mini API still reported `v2.20.0-platform-30-ga5da8ef` with healthy
Consul, Nomad, PostgreSQL, S3, and SOPS. This is copied-data compatibility on
the current candidate code, not a signed release, protected-backup restore,
legacy service fence, rollback, or live promotion.

## 2026-09-26 current PR head

The same checked script passed against a fresh read-only Mini control database
copy using Norn PR #76 commit `0ecfcf05edb8ffe0fe412aa64b4d9e6ff566517d`.
The disposable Darwin/arm64 candidate binary had SHA-256
`4cbc0bbf8de60d0d7228161a05843025df4eaf23fd8daeff8eba8cce755b1b2e`.
Its candidate migration catalog applied versions 1–39, raised the minimum
reader to 5 and writer to 31, and applied zero versions on a second pass.
The private PostgreSQL 17.7 copy contained 28 original tables and 253,463
rows. Original-table counts, primary-key digests, and original-column full-row
digests matched before and after migration. Passive/check startup served the
expected health and schema contract with operation recovery, worker, and
Nomad watcher disabled.

The dump connected through Mini's owner-local PostgreSQL socket with
`default_transaction_read_only=on`. The candidate and script ran only against
a private socket-only PostgreSQL copy; no live database migration, API
replacement, app job, or provider mutation occurred. The rehearsal removed
its private database and scratch files. A separate check found no candidate
transfer directories on either Mac, zero local Docker containers, and the
live Mini API still at `v2.20.0-platform-30-ga5da8ef`.

Read-only Mini inventory also found no active operations and a blocked
production-readiness result. The Mini currently has one Nomad server/client,
one Consul server, no Fleet pools, and several security, offsite backup, and
drill gates open. This copy rehearsal proves candidate schema preservation and
passive startup only; it does not approve protected-master merge, live Mini
upgrade, rollback, remote S3 provider durability, or separate-node MySQL
recovery.
