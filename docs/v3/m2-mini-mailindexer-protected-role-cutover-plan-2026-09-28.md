# M2 protected Mini mailindexer role cutover review

Status: proposed operator sequence; no protected mutation authorized or run.
This is one application database with two registered consumers, not a general
Mini database migration. Re-read every identity below immediately before a
maintenance window; these are 2026-09-28 observations.

| Component | Read-only observed state |
| --- | --- |
| PostgreSQL `mailindexer` | Database owner `norn`; `norn` is a login role, not a superuser. Three idle `norn` sessions were observed, but their allocation origin is unproven. `mail_app` does not yet exist. `vector` extension is owned by `postgres`. |
| Control database `norn_v2` | Also owned and used by `norn`; it must remain available throughout the application role split. |
| `mail-indexer` | Nomad job version 5, modify index 220920, running allocation `7c93974d-6f61-ce1c-e5ad-06eb7ccfce01`, image `mail-indexer:b60b85d1488e-dirty`; task declares `POSTGRES_USER`/`POSTGRES_DB` as `norn`/`mailindexer`. |
| `mail-mcp` | Nomad job version 3, modify index 368342, running allocation `610b2e79-57df-392d-4ca8-b99811046932`, image `mail-mcp:7a5dd2bb8c77-dirty`; task declares a `DATABASE_URL` for `norn`/`mailindexer`. |

The [private-copy receipt](m2-mailindexer-private-shared-role-copy-2026-09-28.md)
now starts with the application database owned by `norn`, transfers database
and all 12 public table/sequence owners to `mail_app`, denies the old role on
the copied app database, runs both consumers' store startup and interaction
write paths, and reverses ownership without losing the copied writes. It
does not prove the deployed dirty images or live allocation drain.

## Preconditions before an operator window

1. Resolve the [Mini control recovery decision](m0-mini-control-recovery-decision.md): RPO, off-host destination, retention and owner. Capture a protected application-database backup too, retrieve it off host, and restore it on a clean private target. A local dump is not a host-loss backup. Record a timed restore and exact roles/ACLs.
2. Reconcile both dirty application checkouts with the deployed images. Retain owner-only exact job specs and running image IDs/bytes with hashes for rollback. The `-dirty` tags do not prove reproducible source. [Draft mail-mcp PR #1](https://github.com/antiartificial/mail-mcp/pull/1) removes its connection fallback but is not merged or deployed; hosted Go CI is blocked before runner steps by account billing.
3. Inventory every writer of `mailindexer`, including jobs outside Norn and direct clients. The three observed `norn` sessions all arrived via loopback and had no application label, so they cannot be assigned to one of the two allocations. Establish a reviewed way to quiesce both jobs and prove zero old sessions. Give replacement URLs distinct `application_name` values for later readback.
4. Prepare the new role and two application credential references in private, without printing passwords. Review the exact `mail-indexer` `POSTGRES_*` and `mail-mcp` `DATABASE_URL` changes together. A new catalog binding must describe the real role and database, and the control-role separation check must pass. Do not activate a catalog that asserts a connection identity the live jobs do not yet use.

## Proposed protected sequence

1. Freeze a before manifest: database/extension/object owners, table counts and key fingerprints, active sessions, app URLs and secret-reference identities, Nomad job versions/modify indexes, allocations, images, routes and health. Recheck backup and restoration receipts. Stop if any identity drifted.
2. Quiesce `mail-indexer` and `mail-mcp` together using reviewed application/Nomad operations. Confirm all old allocations and possible external writers are stopped. End remaining `norn` sessions **for `mailindexer` only** and verify no new one appears. Keep `norn_v2` control sessions healthy. Do not disable the `norn` login globally or run a cluster-wide `REASSIGN OWNED`: that role also owns the control database.
3. On `mailindexer` only, transfer database ownership and its reviewed application tables/sequences to `mail_app`; grant only required schema and object privileges. Remove public connection access and prove `norn` cannot connect to the application database while it still can use `norn_v2`. Record the exact object count and privilege readback. The private copy covered 12 public objects; re-inventory live objects at execution time.
4. Install the reviewed new credentials and exact job specs for both consumers. Start with signed/reproducible images. Verify each allocation reaches the intended database and role, each startup schema path succeeds, and the two distinct `application_name` values appear on live sessions. Run bounded database-backed read/write checks for both consumers, plus their service health and route checks. Keep unrelated Mini workloads running.
5. Record the accepted v3 legacy binding and operator-probed writer baseline only after the deployed jobs' connection identities agree with it. Do not treat catalog activation or a healthy allocation alone as proof of a credential switch. Observe the acceptance window and retain the old image/job/backup artifacts.

Abort before new-role writes if the backup, writer drain, owner/ACL checks or
either app readback fails. Return the original database/object ownership and
job credentials from the retained manifest, then verify both old consumers and
the control database. After new-role writes, the private copy proves that
reverse ownership can retain copied rows; a live reversal still needs an
explicit reconciliation of schema, interactions and external side effects
before restoring old job authority. Do not describe this plan as a completed
protected cutover or sign M2/M5 from the private receipt.
