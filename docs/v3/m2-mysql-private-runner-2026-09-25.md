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

The runner has no HTTP route or enabled public capability. The disposable
PostgreSQL plus MySQL test proves the signed durable intent can reach a MySQL
target through this private path. It is not a complete restore qualification.

## Still required before a usable restore lane

- Independently verify the restored target after `mysql` exits before recording
  a successful operation receipt.
- Renew the operation claim throughout long running imports and stop on lease
  loss before beginning any further external action.
- Hold an explicit maintenance fence that covers application writes and catalog
  activation for the whole restore window.
- Bind source quiescence and retained artifact storage to acceptance; the
  current artifact path remains host-local and ephemeral.
- Reconcile `executing` and `needs-inspection` records after crash, timeout, or
  process loss with an operator-visible, evidence-bound procedure.

These gaps keep MySQL restore non-deployable and the public capability closed.
