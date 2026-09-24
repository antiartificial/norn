# Mini source-to-schema compatibility manifest — 2026-09-24

This is a review manifest for the v3 integration branch at `f9aad413e430addf4f14617e45bde8e3c52beca0`. It identifies the exact observed Mini source and the candidate's immutable migration catalog. It does not certify a Mini upgrade or rollback.

## Observed source and legacy database

The read-only [Mini measurement](m0-mini-measurements-2026-09-24.md) bound the running API binary, via its SHA-256, signed release manifest, and release verifier, to source `a5da8ef15d12e9eca7561e90b90d96f6dc652a21` (`v2.20.0-platform-30-ga5da8ef`). Its local PostgreSQL 17 `norn_v2` database had 28 public control tables and no schema migration ledger. The schema-only dump hash was `6895808a4e773d85f724f58c5db4b8a77ffbec5418c77af0cec49549428c10c5`. No private database rows or credentials are included here.

The candidate's [adoption guard](../../v2/api/store/mini_schema_adoption.go) requires those exact 28 table names and structural fingerprint `568c45d756aab6d7980dc000b34e3a7d5862d30af54967b1aa7811b0e3c0105d` after a schema-only restore to PostgreSQL 16. It refuses incomplete pilot marker tables, structural drift, or any existing Fleet dispatch rows. The adoption transaction repairs the three known legacy columns before applying the immutable baseline ledger. PR #60 exercised the read-only live fingerprint and a private schema-only restore through migration 13; that evidence predates migrations 14–16.

## Candidate migration contract

The current candidate catalog is migrations 1–17, reader contract 3, writer contract 14. These are schema compatibility numbers, not product versions. A writer or reader with an older contract cannot be assumed to run safely after migration 17; rollback and mixed-version windows require separate evidence from a representative data restore.

Checksums below are computed by `store.MigrationChecksum` from the exact `ControlSchemaMigrations()` definitions at `f9aad413e430addf4f14617e45bde8e3c52beca0`. The ledger compares them on every schema check or migration; definitions must not be edited after application.

| Version | Migration | Checksum | Minimum reader | Minimum writer |
| ---: | --- | --- | ---: | ---: |
| 1 | legacy-control-schema-baseline | `6124f0d3fd1e339daea3563f55648d8c0bd4dabbe8ce5f972fccd912ba80d390` | 0 | 0 |
| 2 | atomic-operation-acceptance | `b4894477aa098df50c0afc3d373310342ccf6b2ccb6e6d46c8b01b43f1f250a1` | 1 | 2 |
| 3 | external-effect-recovery | `8fc9df8b9732cab071e0aad5ce53efcd402c98c1507d6ceff041cbef17bff557` | 1 | 3 |
| 4 | operation-execution-checkpoints | `c14847e31286b4679308dd4b7d9fa1375570b5a96fa9e56c64bc17efc2af3542` | 1 | 4 |
| 5 | database-catalog-revisions | `37ed36618266d27000e16b44dc401a9e0f2018781c4a16a64cf83bd15e8ab283` | 1 | 5 |
| 6 | evidence-archive-outbox | `068010b34109e8be94a98de60ed09d3afabff108e588acdbe93460ac55f91e10` | 1 | 5 |
| 7 | evidence-archive-reader-contract | `7ed927881b952cf366b5415fa78cc6de74be6ef6cb354a79d899d6422c9d343c` | 2 | 5 |
| 8 | evidence-reserve-admission | `f47fac7e5b4da8ea703025a83237dfc11183669273c6e75fe213bd17e2115de9` | 2 | 6 |
| 9 | control-event-replay-retention | `83f86234aa3fef131423978488be125b07933d264663a15b178cd05adebde7fb` | 2 | 6 |
| 10 | non-saga-signed-receipt-evidence | `698e04711a17727f46cd788bb33e55508f8f49ab99b09a62d8c07fbfd26e709a` | 2 | 7 |
| 11 | durable-app-desired-replicas | `c3ce6b77e9428e83d8282e06c65c17a13e29c2a85bf374bdb81fc7438dd3cb07` | 2 | 8 |
| 12 | regional-durable-app-desired-replicas | `379253085218d1161710dbc72afb9c227bd6b464da94244ec0d8edf7e2a09ae4` | 2 | 9 |
| 13 | durable-restart-effect-sources | `6bc653a27de50c14482fd840ea4af06f78ffbf4443fd568da2a8b960c1bcd00a` | 2 | 10 |
| 14 | signed-acceptance-byte-reserve | `56cb305a3d4cb2a3d8c0e2b89dd75584dee58cfb0e5c307912b45908456b0e1d` | 2 | 11 |
| 15 | operation-replay-expiry | `43066af7ce262edd8fd2f578f024bb05c6bcd5623721c6028546781337ecb287` | 2 | 12 |
| 16 | snapshot-publication-intents | `3286426f2a2e3b48585596c0c831a27fdbc57587814291063b8da253079eada7` | 2 | 13 |
| 17 | archive-backed-operation-acceptance-retirement | `5e449c3ebaa5f6630bb7c3cdb75fba027581ae83b77ff62551e5951effd59a79` | 3 | 14 |

## Evidence boundary before M0 exit

The local PostgreSQL integration test applies migrations 1–17 to a synthetic unversioned baseline and checks selected legacy rows. The current [private-data migration-17 rehearsal](m0-mini-private-restore-migration17-rehearsal-2026-09-24.md) applied migrations 1–17 to a copied Mini database in a network-disabled PostgreSQL 17 target. All 28 legacy table counts and primary-key fingerprints matched before and after, and a second migration application changed neither the ledger nor compatibility metadata. It did not run either API binary, compare application runtime identity, or exercise rollback after writer contract 14. Old-worker compatibility remains unproven. M0 still needs app/job/route/volume/database ownership, a retained sanitized CI fixture, measured budgets, ADR decisions, and named acceptance.
