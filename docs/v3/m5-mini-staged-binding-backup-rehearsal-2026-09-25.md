# Mini staged-binding protected-backup rehearsal — 2026-09-25

This was a private rehearsal on Mini using an **inactive** encrypted binding
candidate. It did not change the active SOPS file, launcher, API, or control
database. The live service remained `v2.20.0-platform-30-ga5da8ef` and
`/api/health` returned `ok` after the run.

The exact installed v2 source default for `NORN_DATABASE_URL` was verified by
making a read-only connection: database `norn_v2`, role `norn`, loopback. An
owner-only SOPS candidate at
`/Users/0xadb/.config/norn/v3-binding-stage-sxkcbajf/api.env.enc.json`
contains that exact URL and a newly generated 64-character audit signing key.
Its manifest is in the same directory. The candidate and manifest are staged;
the active SOPS ciphertext SHA-256 remained
`ad041897c5fbcc2681ad1bf6371dcd3c67e8ec7eef965ac24080941757d73f78`.
The staged ciphertext SHA-256 remained
`22268a8f1fe9299d70a0a15efacd4052307afe08b0af253d4f68c43269ba7e64`.
Neither the URL nor the key is recorded here.

## Installed v2 signing impact

The installed source at `a5da8ef15d12e9eca7561e90b90d96f6dc652a21`
reads `NORN_AUDIT_SIGNING_KEY` from its environment. Its mutation audit writer
signs completed future receipts when the key is at least 32 characters. Its
integrity reader classifies a completed receipt without a digest as
`unsigned`, independent of the current key; it does not relabel that receipt
`invalid`. A read-only Mini aggregate found 56,399 completed audit rows: all
56,399 had no digest, zero had a key ID, and zero were pending. This supports
adding a first key without a historical-key rotation requirement for this
specific table. It does not prove every other v2 signing consumer or a service
restart is qualified. The staged file must stay inactive until the runtime
binding and restart procedure are reviewed.

The protected-backup producer ran with only those two values decrypted into
its process environment. It read the live PostgreSQL source with a forced
read-only transaction and produced an owner-only custom dump and exact proof.
The independent verifier accepted the 13,192,248-byte artifact with SHA-256
`9acac3e75fa0402c75fe00b8745025aacb58934a693246b3a0992dae21f579f8`.
The proof bound installed release
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21` and the staged database
identity.

The exact candidate source was
`5701212f50e1ec3056ed94729bff56a6442c93ed`. The disposable arm64 binary
was built with `-buildvcs=false -ldflags '-X main.Version=<candidate SHA>'`;
its SHA-256 was
`9f32206fd7f7fadc5d5c2ec3127f98d95a2ac8657a9b53d4be12d16f7eb69551`.
An initial unstamped binary was correctly rejected by the passive version
check; the stamped binary produced the passing result below.

`mini-private-copy-rehearsal` verified the protected bytes, copied them into
owner-only scratch, verified the copy, and restored it to a disposable
PostgreSQL 17.7 Unix-socket instance. It reported:

- 28 original tables and 249,843 original rows;
- matching original primary-key and full-row fingerprints after migrations;
- schema ledger versions 1–38, reader floor 5 and writer floor 30;
- a second successful migrate-only pass and passing passive health;
- verified proof again after the rehearsal, with no source mutation.

The disposable database, backup artifact, proof, tools, and candidate binary
were removed after the run. No `norn-mini-private-copy.*` scratch directory
remained under `/tmp`. The inactive encrypted candidate and manifest remain
owner-only on Mini for review.

This is procedure and data-fidelity evidence **under a staged key**, not a
production-key retained backup. The key is absent from the active launcher
configuration. M5 still needs a reviewed runtime binding, a fresh retained
off-host backup and restore under that binding, an operator-owned workload map,
and the scheduled service-fence/promotion/rollback rehearsal before Mini can
be upgraded.
