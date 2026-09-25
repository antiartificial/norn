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
check was rejected. This is still a private qualification, not a complete
restore lane.

## Still required before a usable restore lane

- Qualify claim theft/loss while SQL is in flight and the resulting inspection
  state, plus bounded memory/disk behavior on representative large data.
- Hold an explicit maintenance fence that covers application writes and catalog
  activation for the whole restore window.
- Bind source quiescence and retained artifact storage to acceptance; the
  current artifact path remains host-local and ephemeral.
- Reconcile `executing` and `needs-inspection` records after crash, timeout, or
  process loss with an operator-visible, evidence-bound procedure.

These gaps keep MySQL restore non-deployable and the public capability closed.
