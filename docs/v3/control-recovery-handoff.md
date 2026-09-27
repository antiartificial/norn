# M1 control recovery implementation handoff

Status: source-reviewed implementation design, not implemented or qualified.
Scope is same-backend PostgreSQL recovery under ADR 0006, not PG/etcd conversion.

## Separate inspection from recovery

Existing application snapshot handlers and data-operation dumps are not control
backups. The HA-lab audit exporter captures an API response, not a consistent
control snapshot. Implement an offline Go command independent of the API queue:

- Inspection export: allowlisted identifiers, counts, redacted projections and
  signature/key references. Explicitly not restorable.
- Encrypted recovery bundle: complete control snapshot, exact signed evidence,
  signed versioned manifest and separately protected recovery secrets.

Use a read-only repeatable-read transaction and an explicit table registry with
primary-key ordering. Preserve integers, timestamps, nulls and binary values
without floating-point conversion. Fail if any control table is unclassified.
Retain PG custom-format dump/restore as the initial recovery engine; canonical
sections and a dump must share an exported PG snapshot if packaged together.

The independently signed manifest binds authority UUID, migration checksums,
compatibility floors, counts, section hashes, relationships and required signing
key IDs. A checksum alone does not authenticate a backup. Restore to an isolated
mutation-disabled database, validate completely, and require separate fenced
activation. Copied session, operation and runner leases grant no authority.

## Required registry coverage

- Authority, schema migration ledger and compatibility state.
- Operations, request identities, acceptance intents and exact canonical bytes,
  signatures, algorithms and key IDs.
- Deployments, regions/datacenters, steps, saga/control events and rollback links.
- Fleet plans/reconciliation, attempts/root/retry lineage and dispatch bindings.
- Devices, grants, token revocations/rotation ancestry, consumed CI assertions,
  enrollments and step-up challenges.
- Exec-session identities and ownership evidence, without reconnecting sessions.
- Webhook deliveries, cron state, notification configuration, audit receipts,
  incidents, recovery drills and all remaining registered control tables.

Acceptance intents store original canonical bytes. Legacy mutation audit stores
fields and digest/key references instead: preserve those original fields. Any
canonical material reconstructed with the existing versioned canonicalizer must
be labeled reconstructed, never historical bytes; never re-sign old receipts.

## Secret boundary

Inspection excludes arbitrary payloads, sensitive commands, ownership tokens,
dispatch nonce, notification secrets/URLs and enrollment/verifier material.
Full signed bytes can contain secrets; redaction invalidates signatures, so only
their references/digests belong in inspection output. Full bytes belong in the
encrypted bundle. HMAC verification keys are secrets, not public metadata.

Recovery also needs independent protected backups of current/retained audit
keys, qualification keys, authentication secrets, GitHub credentials and
relevant object-store/provider configuration. The manifest carries references,
not these secrets. Missing required material blocks verification/activation.

## Acceptance tests

1. Concurrent acceptance during export never creates dangling operation,
   identity, intent or deployment relationships in the snapshot.
2. Round-trip retains exact bytes, IDs, fingerprints and retained-key signature
   verification; same actor/key replays after restore and credential rotation.
3. Changed inputs conflict and foreign actors cannot retrieve prior acceptance;
   revocations and consumed assertions remain effective.
4. Fleet root/retry and deployment rollback links survive restore.
5. Corrupt/truncated sections, unknown schema, broken relationships or missing
   keys block validation before activation.
6. Canary secrets never appear in inspection exports or command logs.
7. Restored workers, schedules and sessions perform no external action before
   explicit fenced activation.

These tests advance M1 recovery/export; they do not qualify a live Mini upgrade,
HA restore, or automatic takeover after external effects.

## Implemented CLI contract (local, unqualified)

`norn-control-recovery restore-passive --recovery-keys-file` takes an age file
encrypted to the bundle identity. Its plaintext is strict JSON:
`{"hmacKeys":["<unpadded base64 HMAC key bytes, >=32>"],"qualificationPublicKeys":["<unpadded base64 Ed25519 or PEM>"]}`.
Key IDs are derived from the material, so substituted keys cannot satisfy the
signed manifest's required key IDs. Passive restore is inert. It runs only
`pg_restore --single-transaction` and read-only validation. It starts no workers,
schedules, sessions or effect executors, and restored leases, owners and runner
attempts grant nothing. Its report is always `activationReady:false` and
includes `unresolvedEffects` for reserved/launched effects that need
reconciliation before any future fenced activation.
