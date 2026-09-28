# M2 Mini mailindexer shared-consumer private role copy

Status: two private-copy passes on Mini; no live job, database, role or secret mutation.

The [reproducible rehearsal](../../v2/scripts/mini-mailindexer-private-shared-role-copy-rehearsal)
(SHA-256 `4b27b2493517b96ac8f1adf6c0a7ed0daedcad3e04862b336ddf762817f4783b`)
made a read-only `pg_dump` of the current `mailindexer` database over Mini's
local PostgreSQL socket. It restored the archive into a separate Postgres.app
17 cluster with a private Unix socket and no TCP listener. The source database
was about 1.8 GB; its archive and scratch remained on Mini and were removed
by the script's exit cleanup.

The copied database had 101,529 `messages` rows. The script transferred all
12 public application tables and sequences from a copied `norn` role to
`mail_app` and verified `12/12` ownership. The old role could no longer
connect to the copied application database. A separate copied control database
remained owned by and accessible to `norn`.

The opt-in `TestPrivateCopiedDatabaseRole` at
[mail-mcp PR #1](https://github.com/antiartificial/mail-mcp/pull/1) connected
as `mail_app`, proved it reached `mail_copy` over the local socket, ran the
app's `InitSchema`, and inserted one `message_interactions` row through the
app's `RecordInteraction` method. The test passed in 0.01 seconds. The script
then returned all 12 copied objects to `norn`, verified the 101,529 messages
and new interaction row remained, denied `mail_app` connection access, and
rechecked the copied control database. A second pass with the full ownership
assertions also passed. A post-run read-only query found the live `mailindexer`
and `norn_v2` database owners unchanged; no rehearsal scratch directory
remained.

This exercises current GitHub-master `mail-mcp` code, not the exact running
image tagged `-dirty`. It does not run `mail-indexer` against the copy, drain
either registered Nomad job, prove both deployed connection URLs switched,
inventory external writers, rotate credentials, or rehearse a protected live
rollback. Those steps remain before M2 catalog activation or an M5 Mini
upgrade can claim unchanged application behavior. The app PR's hosted Go CI
job is blocked before runner steps by GitHub's account billing/spending limit;
its local Go suite passed.
