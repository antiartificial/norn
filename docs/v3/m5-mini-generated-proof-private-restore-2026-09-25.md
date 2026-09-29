# Mini generated-proof private restore rehearsal — 2026-09-25

Status: the protected-backup **script path** passed on a fresh read-only copy
of the live Mini control database. This is not proof of an independently
retained production protected backup, and M5 remains open.

The installed Mini API stayed at release
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21` throughout the run. Its
runtime SOPS environment has neither `NORN_DATABASE_URL` nor
`NORN_AUDIT_SIGNING_KEY`; the legacy API uses its configured local PostgreSQL
default. The rehearsal bound a generated `norn.legacy-control-backup/v1`
proof to that effective control database URL and the exact installed release
SHA, using an owner-only ephemeral signing key. This exercises proof parsing
and HMAC comparison but does not provide a production-key backup identity.

A fresh custom-format `pg_dump` read the Mini control database with
`default_transaction_read_only=on`. The dump and generated proof were
mode `0600` in an owner-only temporary directory. The source dump had
13,078,935 bytes and SHA-256
`69e2c5c93e2b7ea7b8eac66e2b532e48cb88e89709e1235c843932900c8fd935`.
The script verified the proof against those exact bytes, copied the archive
into its own private scratch directory, verified the copy, and restored it
through `pg_restore` into PostgreSQL 17.7 with TCP listening disabled and a
private Unix socket. The source artifact was verified again after all checks.

The disposable candidate was built from exact source commit
`dd54f035ed7a769cb0678034ecf50fd7d0206160`; its binary SHA-256 was
`18929e88b09d518e4af694141fac05aed2ba9aead8a68690f8a30d085f007ab1`.
The restored copy had 28 original public tables and 247,691 rows. Every
original table's row count and ordered primary-key fingerprint matched after
two migrate-only passes. The schema ledger contained versions 1 through 23,
with minimum reader 4 and writer 19. The candidate's passive/check API
returned the exact candidate version and expected health/schema fields with
operation recovery, worker, and Nomad watcher disabled.

The rehearsal script stopped the passive API and private PostgreSQL cluster
and verified removal of its scratch directory. The outer runner removed the
generated dump, proof, key environment, candidate, and staging directory.
A separate remote check found zero matching Mini temporary directories; local
staging was also removed. The live API remained healthy and reported the same
installed version after the run. No live database migration, API restart,
worker claim, or promotion was performed.

The next M5 gate is a separately retained, protected backup with its own
production-key proof and successful restore, followed by scheduled Mini
maintenance covering the actual one-way legacy service fence, candidate
promotion, traffic observation, and roll-forward procedure. This generated
artifact was intentionally deleted and cannot satisfy that backup gate.
