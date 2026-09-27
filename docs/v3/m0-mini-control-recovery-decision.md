# M0 Mini control recovery decision

Status: decision requested; neither option below is implemented or qualified.
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

## Decision options

| Option | User-visible data-loss bound after a host/storage loss | Implementation to qualify | Practical trade-off |
| --- | --- | --- | --- |
| A: retain ≤15-minute backup-only RPO | At most 15 minutes **only after** continuous off-host WAL shipping and PITR are proven over the full interval | Configure Postgres.app WAL archive and a protected off-host repository; bound archive delay and backlog; verify exact-WAL continuity, timestamped restore, source-host loss, credentials, encryption, retention and alerting | More moving parts on the Mini, but preserves the draft recovery target |
| B: accept ≤24-hour backup-only RPO | Up to 24 hours **only after** a daily off-host backup and its completion deadline are proven | Scheduled, encrypted, versioned off-host full backup; monitor missed/failed runs and capacity; restore from the remote object onto a clean private host | Simpler initial operations, with materially more possible control-history loss |

Neither cadence proves an RTO. Both require a timed protected-backup retrieval,
private restore, exact candidate transition, identity/auth/history checks, and
documented rollback decision. The owner must choose an option, off-host
destination and retention period, then sign the measured result. Until then,
M0 and M5 stay open.

The current `norn.legacy-control-backup/v1` producer and verifier serve the
**fresh M5 upgrade transition**. The verifier rejects proofs older than one
hour by default (`NORN_LEGACY_BACKUP_MAX_AGE_SECONDS=3600`). That freshness
guard must remain for the transition. It is not a recurring off-host backup
catalog or a general disaster-recovery restore path: a daily retained backup
would normally be older than the transition limit. Whichever RPO option is
chosen needs its own backup inventory, retention and age policy, remote
retrieval, and clean-host restore test. Do not widen the transition proof's
age limit to claim disaster recovery.

Recommendation for the release: retain **A** if the intended v3 recovery
contract truly needs 15-minute loss bounds. If a 24-hour loss window is
acceptable for this single-machine Mini, explicitly revise the release
contract to **B** and communicate that weaker guarantee before adoption.
Do not describe the Linux HA lab's pgBackRest result as a qualified
Postgres.app configuration.
