# MySQL private restore runner: supervision slice

The private `MySQLRestoreRunner` consumes an already accepted, prepared
`database.mysql-restore` operation. It commits the one-way `executing` intent
before starting `mysql`, passes the already verified SQL artifact descriptor as
stdin, creates TLS and password files only in the session's owner-only
directory, and terminalizes the intent with its operation receipt. Failure
after the one-way boundary records `needs-inspection`; a worker crash leaves
`executing` and cannot be retried as SQL.

The client executable is checksum-verified through an opened descriptor before
execution and its inode is compared again afterward. The process still executes
the configured path because macOS clients may load libraries relative to that
installed path. This leaves a pathname-resolution race that this slice cannot
eliminate portably. A detected path or inode change is an uncertain restore and
is retained for inspection. Deploy the tool only from a protected installation
root; do not treat the checksum check as a complete executable-attestation
system.

The runner has no HTTP route or enabled public capability. A v2 artifact binds
source-derived SHA-256 schema and ordered-row fingerprints into its signed
intent. The runner independently inspects the restored target before recording
success. It renews its operation claim throughout import and cancels the SQL
client on known renewal loss. A disposable PostgreSQL 17.7 plus MySQL 8.4 run
passed with a 120 ms lease and a 350 ms delayed client; a changed-row target
check was rejected. A second disposable run stole the claim during a delayed
client: cancellation returned promptly, no success receipt was written, the
intent became `needs-inspection`, and successor replay was rejected. The
private runner bounds pipe draining after cancellation so a wrapper child
cannot delay that containment indefinitely. This is still a private
qualification, not a complete restore lane.

Expired executing intents now atomically become `needs-inspection` with a
failed, manual-recovery operation. A read-scoped inspection endpoint returns
verified signed acceptance, catalog, target, and artifact identities without
the private artifact path or profile selector. It cannot retry or acknowledge
an ambiguous restore. A `prepared` intent whose claim expired before SQL is
atomically failed and its target reservation released; it cannot be confused
with an executing import. The disposable PostgreSQL recovery test passed both
cases.

The private request now signs a source-quiescence evidence reference and
persists a maintenance fence from preparation through execution. Catalog
activation refuses while any such fence is held. Successful completion releases
the fence in the receipt transaction; an expired prepared intent releases it
before SQL, while ambiguous execution retains it. This fence has no production
application write or write-resume consumer yet. The signed evidence reference
records what an operator asserted; it does not independently prove quiescence.

A disposable PostgreSQL/MySQL rehearsal killed the separate restore worker and
its mysql child with OS SIGKILL after the target had received the first table.
Fresh control-store recovery retained `needs-inspection`, a failed one-attempt
operation, signed redacted inspection, and no automatic SQL replay. This proves
the crash classification at that boundary, not safe application write isolation
or a complete rollback procedure.

## Still required before a usable restore lane

- Qualify bounded memory/disk behavior on representative large data.
- Wire the durable maintenance fence into application write and write-resume
  paths, and prove quiescence on the actual managed WordPress/MySQL runtime.
- Bind source quiescence and retained artifact storage to acceptance; the
  current artifact path remains host-local and ephemeral.
- Define evidence-bound operator reconciliation after inspection. Current
  signed identity and target fingerprints cannot prove whether a partial SQL
  prefix was applied, so no acknowledgement mutation is exposed.

These gaps keep MySQL restore non-deployable and the public capability closed.
