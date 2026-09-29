# M0 Mini control recovery decision

Status: 2026-09-28 scope decision recorded. The Mini is a development machine;
an on-host backup is acceptable for its v3 upgrade. No Mini disaster-recovery
RPO or RTO is promised. Production recovery remains a separate qualification.
This decision concerns the Mini's **Norn control PostgreSQL**. Application
databases, volumes, evidence archives, Nomad/Consul state, and Fleet etcd
require their own recovery contracts.

## Current evidence

The 2026-09-26 [control-store measurement](m0-mini-control-budget-proposal-2026-09-26.md)
found a 238.45 MiB control database, about 4.04 MiB/day of observed growth,
`archive_mode=off`, and no evidenced recurring off-host control backup. A
private local dump/restore was fast, but no protected production-key off-host
restore or end-to-end RTO has passed. The earlier release draft said RTO ≤30
minutes and backup-only RPO ≤15 minutes; neither was qualified and neither
applies to this dev Mini gate.

A later read-only Mini check on 2026-09-26 still found `archive_mode=off` and
`archive_timeout=0`; no PostgreSQL control-backup job was found in the inspected
user schedule. A separate [synthetic Postgres.app PITR fixture](m0-mini-postgresapp-disposable-pitr-2026-09-26.md)
proved local Mac base-backup/WAL recovery mechanics, including a timestamped
recovery boundary. It did not use off-host storage or production control data.
A separate [Mini-to-Mac synthetic logical restore](m0-mini-separate-host-logical-restore-2026-09-26.md)
passed the transport and clean-target restore step on another host. It used a
temporary source cluster, not the control database or the protected backup
catalog, and did not preserve production roles or ACLs.

A read-only Mini refresh on 2026-09-27 at approximately 17:00 UTC still showed
PostgreSQL `archive_mode=off`, `archive_timeout=0`, and zero archived/failed WAL
files in `pg_stat_archiver`. `tmutil destinationinfo` reported no Time Machine
destination configured, and `tmutil latestbackup` could not mount a backup
destination. This does not exclude an unrelated external backup system, but
neither previously proposed control RPO had an evidenced off-host mechanism.

On 2026-09-28, a fresh read-only check of Mini's owner
`~/.config/norn/backups` directory found only older control dumps dated
2026-09-10 and unrelated backup entries. The inspected owner LaunchAgents and
system LaunchDaemons had no scheduled Norn control-backup job; the listed Norn
agents were the API, cloudflared, Consul, host agent, host assurance,
supervisor and Nomad. The separate [same-day control baseline](m0-mini-baseline-refresh-2026-09-28.md)
still found `archive_mode=off`, `archive_timeout=0`, and zero archived WAL
files. These are bounded local observations, not an inventory of every remote
backup service or storage destination. No protected off-host restore receipt
has been presented for either former RPO option.

At 2026-09-28 09:19 UTC, another read-only query of the owner-local Mini
control database still reported `archive_mode=off`, `archive_timeout=0`, and
zero archived and failed WAL files. A names-only inspection of the encrypted
API configuration found S3/Garage variables but no
`NORN_AUDIT_SIGNING_KEY` or `NORN_DATABASE_URL`; the inspected launchd
environment also lacked those two values. The S3 names do not establish an
off-host control-backup destination. The stable audit key and exact database
URL are separate prerequisites for the protected M5 transition proof.

## Dev Mini decision and production boundary

For M0/M5, a fresh control backup in a directory on the Mini is acceptable.
The protected upgrade still needs a named backup path, readable manifest,
freshness and integrity verification, private restore with the actual control
data/roles/ACLs and required keys, and a documented rollback decision before
the live transition. A backup on the same machine can be lost with that
machine or disk, so no host-loss recovery or bounded-loss claim follows from
it. Application databases and other state have separate transition and
recovery requirements.

For any future production control plane, minimize data loss with an explicitly
chosen RPO and an off-host, tested restore design. The earlier ≤15-minute
backup-only RPO and ≤30-minute RTO are **draft production targets**, not signed
service guarantees. Continuous off-host WAL/PITR is a candidate for the
15-minute target; it needs measured archive lag, retention, monitoring and a
source-host-loss restore. Production owners must accept the targets and
qualify them on the actual topology before claiming them.

The current `norn.legacy-control-backup/v1` producer and verifier serve the
**fresh M5 upgrade transition**. The verifier rejects proofs older than one
hour by default (`NORN_LEGACY_BACKUP_MAX_AGE_SECONDS=3600`). That freshness
guard must remain for the transition. It is not a recurring disaster-recovery
catalog. A future production backup system needs its own inventory, retention,
age policy, remote retrieval and clean-host restore test. Do not widen the
transition proof's age limit to claim disaster recovery. Do not describe the
Linux HA lab's pgBackRest result as a qualified Postgres.app configuration.
