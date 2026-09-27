# Claude implementation handoff

Continue the existing Norn v3 implementation in this dirty worktree. Preserve every existing change. Read AGENTS.md, docs/v3/README.md, execution-milestones.md, implementation-status.md, planning-contracts.md and the relevant ADRs/handoff documents before editing. The full roadmap remains M0–M9; passing a foundation test does not complete it.

## Authority and first batch

Implement and test locally only. No commits, pushes, PRs, deployments, SSH, cloud/provider operations, production credentials, package installs, destructive cleanup, or edits outside this checkout. Do not touch sibling norn, norn-fleet, or NornUI repositories. Do not weaken tests or claim unexercised runtime guarantees. Existing changes belong to the user. No agent attribution in commits. Stop at a coherent reviewable batch and report exact files, commands, results, remaining gaps.

For this first substantial batch, finish the pure M2 database resolver and transition contract, then close control-recovery verifier/CLI gaps described below. Do not edit effect/supervisor or pipeline runtime wiring in this batch. These form the next staged batch after independent review. Do not spawn additional agents; perform the implementation directly.

## M2 database resolver

Read database-binding-handoff.md and ADR 0003. Only database/types.go exists so far; no resolver/tests. Correct ClientCertificateRequired to reflect configured service policy, not presence of certificate material. Implement catalog validation, named versus explicit legacy resolution, strict PG/MySQL capability separation (reject Cockroach), application/control separation, redacted inspection/errors, full expected target identity fencing, and ValidateTransition. Identity includes service ID/generation and binding ID/generation, engine/database/role. Provider/engine/version/topology/TLS-policy changes require service generation bump; binding target changes require binding generation bump. Credential-only rotation may retain generation. Reject ambiguous named+legacy fallback and unsafe removals. Keep deployment profile separate from NORN_PROFILE security. Test same database names on two services, Mini explicit legacy, all stale generations, transition bumps, rotation, missing refs/defaults, TLS policy versus verification, secret canaries in JSON/errors, and unsupported engines/purposes. No parser/runtime consumer wiring yet.

## Control recovery follow-through

Read control-recovery-handoff.md and current code/tests. Real encrypted pg_dump -> verify -> pg_restore passive roundtrip currently passes. Retain frozen schema migration 3 checksum and exact bigint handling. Remaining work:

- Qualification verifier must enforce handler-equivalent structure, outer schema/environment/IDs/provenance; add actual producer-compatible fixture and App/SourceSHA/Candidate tamper cases.
- Audit fixture currently uses package signAudit: add independent producer/known-vector compatibility.
- Acceptance verification duplicates store invariants: test admission, fleet reconciliation and runner variants and avoid drift where a shared pure verifier is safe.
- Add CLI end-to-end encrypted recovery-key-document and passive restore invocation tests; document key JSON and passive/inert effect semantics.
- Test midstream pg_restore failure transaction rollback, not only /usr/bin/false before writes. Test dump midstream failure publishes no artifact.
- Private key/DSN file reads already single-fd O_NOFOLLOW with modes; inspection output atomic no-replace. Preserve these guarantees and inspect other file readers for appropriate handling.

## Local verification resources

Disposable PostgreSQL is already running on Unix socket /tmp/norn-v3-pg.AEaWuh port 55439, user arti, no password/no TCP. Source test URL: postgresql://arti@/norn_v3_verify_acceptance_20260922a?host=/tmp/norn-v3-pg.AEaWuh&port=55439 . Recovery target: postgresql://arti@/norn_v3_recovery_verify_20260922_root_a?host=/tmp/norn-v3-pg.AEaWuh&port=55439 . Use NORN_TEST_DATABASE_URL and NORN_TEST_RECOVERY_TARGET_DATABASE_URL. Tests must use isolated schemas, never delete whole source/target database or stop the shared cluster.

Latest recovery race suite with both URLs passed. Earlier full API race suite passed with only exact pre-existing TestSampleDarwinHostMetrics excluded (Darwin sampler killed by environment). Rerun relevant race suites and full API suite after changes. UI previously passed 110 Vitest + 4 fixture tests and build; no UI changes needed for this batch.

## Next batch context (do not implement yet)

Effect/supervisor is compiling WIP, not production-ready. Needs replay-after-journal-loss tests; concurrent root registry/tamper tests; bounded output and fsynced result with signed digest/length; log fd lifecycle fix; cgroup fake-FS tests and actual scoped Linux qualification; pipeline pending propagation without premature terminalization; stable original execution across claims; main/config and atomic helper installation. Darwin containment remains fail-closed, Docker/BuildKit excluded. Worker deferral alone is not end-to-end recovery. Never equate stopped shell with safe repeat of remote effects.

Fleet external runner still lacks Idempotency-Key in sibling repo; recorded in atomic-acceptance-handoff.md, not authorized for this batch. Native UI event-overflow handling and live browser resync qualification remain unimplemented/unverified. No full milestone qualification or production readiness has been established.
