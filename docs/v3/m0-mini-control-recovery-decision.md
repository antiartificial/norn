# M0 Mini control recovery decision

Status: the owner selected the 15-minute target for the development Mini and
accepts a 24-hour fallback if the tighter mechanism proves disproportionate.
A frequent full-dump path to a temporary personal Space has started, and one
remote object restored on a separate Mac. Sustained RPO, host-loss recovery,
and end-to-end RTO remain unqualified. See [RESUME.md](RESUME.md) for current
operation and cleanup.
This decision concerns the Mini's **Norn control PostgreSQL**. Application
databases, volumes, evidence archives, Nomad/Consul state, and Fleet etcd
require their own recovery contracts.

## Current evidence

The 2026-09-26 [control-store measurement](m0-mini-control-budget-proposal-2026-09-26.md)
found a 238.45 MiB control database, about 4.04 MiB/day of observed growth,
`archive_mode=off`, and no evidenced recurring off-host control backup. A
private local dump/restore was fast, but no protected production-key off-host
restore or end-to-end RTO has passed. The release draft says RTO ≤30 minutes
and backup-only RPO ≤15 minutes; both remain unqualified.

A later read-only Mini check on 2026-09-26 still found `archive_mode=off` and
`archive_timeout=0`; no PostgreSQL control-backup job was found in the inspected
user schedule. A separate [synthetic Postgres.app PITR fixture](m0-mini-postgresapp-disposable-pitr-2026-09-26.md)
proved local Mac base-backup/WAL recovery mechanics, including a timestamped
recovery boundary. It did not use off-host storage or production control data.
A separate [Mini-to-Mac synthetic logical restore](m0-mini-separate-host-logical-restore-2026-09-26.md)
passed the transport and clean-target restore step on another host. It used a
temporary source cluster, not the control database or the protected backup
catalog, and did not preserve production roles or ACLs.

## Decision options

| Option | User-visible data-loss bound after a host/storage loss | Implementation to qualify | Practical trade-off |
| --- | --- | --- | --- |
| A: retain ≤15-minute backup-only RPO | At most 15 minutes **only after** a complete off-host backup or recoverable WAL position is proved within every interval | Qualify frequent full dumps or continuous WAL shipping over the full interval; bound delay and backlog; verify source-host loss, credentials, retention and alerting | More moving parts on the Mini, but preserves the draft recovery target |
| B: accept ≤24-hour backup-only RPO | Up to 24 hours **only after** a daily off-host backup and its completion deadline are proven | Scheduled, encrypted, versioned off-host full backup; monitor missed/failed runs and capacity; restore from the remote object onto a clean private host | Simpler initial operations, with materially more possible control-history loss |

Neither cadence proves an RTO. Both require a timed protected-backup retrieval,
private restore, exact candidate transition, identity/auth/history checks, and
documented rollback decision. The interim Space retains `control/` objects
for seven days and local copies for 48 hours. These are temporary development
settings, not an accepted release budget. M0 and M5 stay open.

The current `norn.legacy-control-backup/v1` producer and verifier serve the
**fresh M5 upgrade transition**. The verifier rejects proofs older than one
hour by default (`NORN_LEGACY_BACKUP_MAX_AGE_SECONDS=3600`). That freshness
guard must remain for the transition. It is not a recurring off-host backup
catalog or a general disaster-recovery restore path: a daily retained backup
would normally be older than the transition limit. Whichever RPO option is
chosen needs its own backup inventory, retention and age policy, remote
retrieval, and clean-host restore test. Do not widen the transition proof's
age limit to claim disaster recovery.

For the release, retain **A** only if the 15-minute loss bound is measured
over representative operation, including sleep and scheduler behavior. If the
owner selects the 24-hour fallback, explicitly revise the release contract
before adoption.
Do not describe the Linux HA lab's pgBackRest result as a qualified
Postgres.app configuration.
