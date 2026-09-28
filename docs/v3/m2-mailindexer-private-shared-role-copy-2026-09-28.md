# M2 Mini mailindexer shared-consumer private role copy

Status: three private-copy passes on Mini; no live job, database, role or secret mutation.

The [reproducible rehearsal](../../v2/scripts/mini-mailindexer-private-shared-role-copy-rehearsal)
(SHA-256 `bb8471dec79d7ba0b0de86db5c5d29d5a29fe222e380d590e9335d3ee95ef117`)
made a read-only `pg_dump` of the current `mailindexer` database over Mini's
local PostgreSQL socket. It restored the archive into a separate Postgres.app
17 cluster with a private Unix socket and no TCP listener. The source database
was about 1.8 GB; its archive and scratch remained on Mini and were removed
by the script's exit cleanup.

The latest copied database had 101,532 `messages` rows (up from 101,529 in
the earlier passes as the live source continued operating). The script transferred all
12 public application tables and sequences from a copied `norn` role to
`mail_app` and verified `12/12` ownership. The old role could no longer
connect to the copied application database. A separate copied control database
remained owned by and accessible to `norn`.

The opt-in `TestPrivateCopiedDatabaseRole` at
[mail-mcp PR #1](https://github.com/antiartificial/mail-mcp/pull/1) connected
as `mail_app`, proved it reached `mail_copy` over the local socket, ran the
app's `InitSchema`, and inserted one `message_interactions` row through the
app's `RecordInteraction` method. The test passed in 0.01 seconds. A temporary
probe inside the dirty `mail-indexer` checkout then ran its actual store
`NewStore`, full `InitSchema` and `RecordInteraction` methods as `mail_app`
against the same copy; it passed. The script returned all 12 copied objects
to `norn`, verified the 101,532 messages and both new interaction rows
remained, denied `mail_app` connection access, and rechecked the copied
control database. The probe directory was removed, leaving the original
dirty checkout unchanged. A post-run read-only query found live `mailindexer`
and `norn_v2` database owners unchanged; no rehearsal scratch directory
remained.

This exercises current GitHub-master `mail-mcp` code and the current dirty
`mail-indexer` checkout, not either exact running image tagged `-dirty`. It
does not run either registered Nomad job against the copy, prove both deployed
connection identities switched, inventory external writers, drain old
allocations, rotate credentials, or rehearse a protected live rollback. Those
steps remain before M2 catalog activation or an M5 Mini upgrade can claim
unchanged application behavior. The `mail-mcp` PR's hosted Go CI job is
blocked before runner steps by GitHub's account billing/spending limit; its
local Go suite passed.
