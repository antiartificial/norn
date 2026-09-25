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
