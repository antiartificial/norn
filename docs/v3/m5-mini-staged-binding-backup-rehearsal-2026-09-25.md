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

## Read-only binding preflight — 2026-09-27 UTC

The active and staged SOPS ciphertext digests still match the values above.
Both files are owner-owned mode `0600`; launchd still runs
`/Users/0xadb/bin/norn-api-sops-launcher`. Decrypting both files only inside
Mini showed that the staged candidate adds the explicit loopback `norn_v2`
PostgreSQL URL and a 64-character audit key, while all other decrypted fields
match the active file. The active file still has neither explicit value. No
plaintext URL, key, or token was emitted or copied. The staged file remained
inactive, and no service was restarted.

At installed source `a5da8ef15d12e9eca7561e90b90d96f6dc652a21`, the
audit key also signs future mutation receipts and Fleet capacity plans and is
used to verify signed audit incidents and plans. A read-only query through the
staged URL reached database `norn_v2` as role `norn`; it found 58,556 mutation
audit rows, zero with a digest or key ID, zero mutation audit incidents, and
zero stored `fleet.capacity-plan` operations. This closes the historical
signature inventory for those installed V2 consumers at this instant. Retain
the staged key as a protected recovery input, and repeat the inventory before
activation in case new rows appear. Re-run the binding doctor after activation
and before producing the production-key backup. This remains an M5 preflight,
not approval to switch the launcher or promote V3.

## Read-only Mini refresh — 2026-09-27 20:21 UTC

The live API still reported `ok` and
`v2.20.0-platform-30-ga5da8ef`; `current` still named release
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. The active and staged
encrypted-file SHA-256 values still matched the digests above. Decryption
inside Mini confirmed the active launcher environment still lacks the explicit
database URL and audit key, while the inactive staged file still contains
both. No value was printed or copied, and the service was not restarted.

A read-only query through the staged binding found 59,703 mutation audit
events, none with a digest or key ID, zero audit incidents, and zero
`fleet.capacity-plan` operations. This refresh supports the first-key
transition for those installed V2 consumers at this time; repeat it just
before activation because the audit table continues to grow.

The authenticated control inventory reported zero active operations, host
status `ok`, and no configured Fleet node pools. It returned 27 app records
for 26 unique names (Watchtower appeared twice): 18 records healthy and nine
unhealthy. Five unhealthy apps declare `deploy: false`. Four declare
`deploy: true` but have no healthy allocation: `ad-asset-verifier`,
`ft-trove`, `hello-norn`, and `its-alive-api`. Thirteen incidents remained
open, including health-critical history for `turnkey-offer-intake`,
`vigil-gateway`, `mail-mcp`, `like-trove`, and `its-alive-api`, plus a capacity
warning. A currently healthy allocation does not close its historical
incident or prove uninterrupted service.

Before the scheduled maintenance rehearsal, the operator must classify the
four deploy-enabled absent workloads and the open incidents as intended
suspension, existing fault, or rehearsal blocker. Capture an exact pre/post
job, route, endpoint and database map. The live Mini checkout still lacks the
new protected-backup helper; use the exact reviewed candidate tooling in the
protected procedure. The staged binding remains inactive, and M5 remains
open.

### Deploy-enabled workloads without a healthy allocation

At 2026-09-27 20:24 UTC, authenticated, app-filtered deployment history
showed the following most recent records. These are historical deployment
results, not proof that the workload is running now.

| App | Latest deployment | Current runtime | Rehearsal disposition |
| --- | --- | --- | --- |
| `ad-asset-verifier` | Deployed 2026-05-19 | No Nomad allocation | Unclassified absence; owner must confirm whether suspension is intended. |
| `ft-trove` | Deployed 2026-07-10 after earlier same-day failures | No Nomad allocation | Unclassified absence; owner must confirm whether suspension is intended. |
| `hello-norn` | Deployed 2026-02-13 | No Nomad allocation | Unclassified absence; owner must confirm whether suspension is intended. |
| `its-alive-api` | Failed deployment begun 2026-09-04; failed region with zero active weight | Nomad job `dead`, no allocation | Existing failed state with open restart, auto-rollback, and critical-health incidents; investigate or explicitly exclude before the rehearsal. |

The first three have a successful latest deployment, but that does not prove
an intentional stop or continuing health. Keep all four out of an
"unchanged healthy workload" assertion until their owners record the intended
state and expected post-upgrade behavior.
