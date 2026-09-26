# M5 current-head Mini private-copy rehearsal — 2026-09-25

## 2026-09-26 job and route definition preservation

PR #76 commit `d8dec11fe9e8982e8295ed693023df74ed71d9a9` added an
optional read-only live-state check to the private-copy script. A
Darwin/arm64 candidate built with that exact SHA embedded had binary SHA-256
`0376eb7203c72e4a35e59534ac0811d9a2d9e278112224c2042905b91677df6a`.
The checked script on Mini had SHA-256
`59a8bf607635e80afbfb322cc3cff643050aa3edf3dfebd0b4914f7d89995036`.

The rehearsal read Mini's 37 base Nomad job IDs and `JobModifyIndex` values,
plus the bytes of the owner-local cloudflared config, immediately before and
after the isolated candidate run. Both fingerprints matched. Periodic child
jobs were excluded; allocation health and external route traffic were not
part of this comparison. The source dump used a read-only PostgreSQL session.
The private PostgreSQL 17.7 copy contained 28 original tables and 254,983
rows. Candidate migrations 1–43, a second migrate-only pass, original-row
count/key/full-row fingerprints, and passive health/schema checks all passed;
the resulting reader/writer floor was 5/31. The private database and scratch
were removed and verified absent. Transferred candidate/script files and the
local build were removed. The installed Mini API still reported signed
`v2.20.0-platform-30-ga5da8ef` afterward.

This closes the narrow private-copy check that base job definitions and the
cloudflared config did not change during this rehearsal. It does not prove
application data restore, allocation continuity through a real upgrade,
public route availability, protected off-host backup, the one-way legacy
fence, rollback, or M5 sign-off.

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

## Current PR head through migration 43 — 2026-09-26

The checked rehearsal passed again against a fresh read-only Mini dump at Norn
PR #76 commit `1159645e99bdbd214866e14c15e964607e2e11f5`. The disposable
Darwin/arm64 candidate was built with that exact SHA embedded in `main.Version`;
its SHA-256 was
`47faf42d56eafb1fbb7926ad2d74444fcbea9d496c2520246345e4bc0daf6657`.
An initial candidate built without the embedded version reached passive startup
but failed the expected `/api/version` assertion. It was replaced before this
passing run; the failure was a build-label mismatch, not evidence of a failed
migration.

The source connected over Mini's owner-local PostgreSQL socket with
`default_transaction_read_only=on`. The private PostgreSQL 17.7 copy contained
28 original public tables and 254,755 rows. The candidate applied migrations
1–43, accepted a second migrate-only pass, and preserved every original table's
row count, ordered primary-key digest, and original-column full-row digest.
The resulting compatibility floor was reader 5, writer 31. Passive/check
startup passed health, exact version, schema, and disabled recovery/worker/
watcher assertions.

The private database, dump, logs, transferred candidate, and local build were
removed and verified absent. Mini's live API still reported
`v2.20.0-platform-30-ga5da8ef` with Consul, Nomad, PostgreSQL, S3, and SOPS
up. This is current-head copied-data schema and passive-startup evidence. It
does not prove protected backup restore, application/job/route preservation,
installed-binary rollback, or a live Mini upgrade; M5 remains open.

## Source-URL fidelity and exact-head refresh — 2026-09-26

After the rehearsal script began preserving supported libpq URL parameters and
rejecting unknown or repeated parameters, it passed against another fresh
read-only Mini source copy at PR #76 commit
`a598196b5da21c36669b03c0e3542460450fec89`. The disposable Darwin/arm64
binary was built with that SHA embedded in `main.Version`; its SHA-256 was
`6b1955935cf62f994726a4ba4cbb86a3e61c4a95e8e481b58f22c788f0a167be`.
The source was accessed through Mini's owner-local PostgreSQL socket as the
owner account, with `default_transaction_read_only=on` and no database
password in the command.

The private PostgreSQL 17.7 copy had 28 original public tables and 254,849
rows. Migrations 1–43, the second migrate-only pass, every original table's
row count, ordered primary-key digest, original-column full-row digest, and
passive health/version/schema checks passed. The compatibility floor remained
reader 5 and writer 31. The rehearsal reported cleanup of its private database,
dump, and logs. The transferred candidate/script directory on Mini and local
build directory were removed and independently verified absent. Mini's live API
still reported `v2.20.0-platform-30-ga5da8ef` and retained its original API
process.

This verifies the revised read-only source path with Mini's actual Unix-socket
URL. It does not exercise remote TLS parameters against a live source, a
protected production-key backup, installed-binary rollback, live application
traffic, or the scheduled one-way maintenance transition. M5 remains open.
