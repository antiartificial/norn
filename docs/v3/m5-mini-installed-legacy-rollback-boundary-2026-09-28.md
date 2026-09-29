# M5 installed Mini legacy rollback boundary

Status: read-only runtime assessment with one unintended legacy startup
attempt; no protected upgrade or rollback was performed.

On 2026-09-28, Mini's `current` release symlink resolved to
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. A direct invocation of
that installed `norn-api` with `--norn-startup-contract` was **not** a safe
capability probe: this v2 binary did not recognize the flag and entered its
normal startup path. It logged a worker start, then exited because the live
API already owned port 8800. The binary was not invoked again. The running
API subsequently returned HTTP 200 at `/healthz` and HTTP 404 at
`/api/schema`, confirming that the installed legacy server does not expose
the v3 schema/startup contract through that route.

Follow-up read-only queries found no `operations` rows started or updated,
and no `mutation_audit_events` rows started, between 09:59:30 and 10:00:00
America/Chicago around the attempt. The registered `mail-indexer` and
`mail-mcp` jobs retained their previously observed versions and modify
indexes (5/220920 and 3/368342). These checks bound what was observed; they
do not prove the brief worker made no external effect.

Do not invoke an untrusted legacy binary with a prospective probe flag in the
owner environment. The `platform-upgrade` probe now rejects binaries without
the static `norn.startup/v2` marker **before** invoking them; a focused test
proved a marker-free executable with a side effect was not run, while a
marker-bearing candidate still reached passive preflight. This marker is an
early refusal, not proof of safe binary behavior. The subsequent runtime
probe uses a scrubbed environment, an unreachable database URL, bounded
waiting and process termination. Its `legacy-baseline` path requires an
authenticated operation drain and fences the old binary before candidate
promotion; it does not claim
the installed v2 binary can restart after schema migration. The protected M5
runbook must use that boundary or a separately qualified compatible old
release, with production-key backup/restore and operator-reviewed rollback
conditions. The private schema-47 copy alone cannot prove old-binary rollback.
