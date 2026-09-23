# M2 retention implementation handoff

Status: source-reviewed implementation preparation, 2026-09-22. No archive,
collector or pruning qualification is implied. Parent: [ADR 0001](adrs/0001-state-evidence-and-observability.md).

## Separate logs from evidence

`v2/api/handler/logs.go` already streams application output from Nomad, rather
than PostgreSQL. Allocation/task-aware collection, rotation, spool limits and
authenticated historical queries remain a distinct M2 workstream. Moving saga
and operation history out of PG is evidence archival, not stdout log rotation.

## Source-derived retention holds

### Collector and bounded-capture implementation entry points

Source recheck during batch-four review:

- `nomad/logs.go:StreamLogs` selects one running allocation and the first task
  found in the job, then merges stdout/stderr without source labels. Replace
  this implicit selection with allocation/task/stream identity for collection;
  historical retrieval must retain those labels and handle allocation turnover.
- The stream creates a Nomad cancellation channel but returning/closing the
  pipe reader does not close that channel. Collection must cancel upstream on
  request cancellation, consumer close and write failure. Closed frame/error
  channels must be removed from the select loop to avoid busy spinning.
- No explicit `LogConfig`, `MaxFiles` or `MaxFileSizeMB` occurs in the current
  Nomad translation package. Define and test actual submitted rotation limits;
  do not infer bounded platform retention from undocumented runtime defaults.
- `worker/maintenance.go:CommandMaintenanceExecutor.Execute` uses
  `CombinedOutput` and only truncates to 64 KiB after exit. This bounds persisted
  metadata, not process memory. Stream into a bounded capture/spool while
  draining output; preserve exit status and explicit truncation metadata.
- Migration and other subprocess call sites also use `CombinedOutput`.
  Inventory their output destinations before archival: diagnostic bytes,
  durable outcome evidence and credentials need different treatment. Test
  high-volume output and slow/offline archive sinks without unbounded memory.

These are next-vertical implementation inputs, not changes to the active
database correction batch and not evidence of collector qualification.

| State | Required hold before pruning |
| --- | --- |
| Operations and acceptance identities/intents | Active, indeterminate and manual-recovery work; replay identities until an explicit expiry contract; original signed bytes and verified archive-backed resolution |
| Deployments, regions and steps | Current/rollback candidates, routing state, unresolved steps and retry-safety checkpoints; a closed dependency graph, not merely terminal status |
| Fleet attempts and dispatch bindings | Root/retry lineage, active attempts, approval/nonce correlation and recoverable external-run identity |
| Saga events | Closed evidence cutoff and archive-aware `ListBySaga`; publication can append after terminalization |
| Control events | Explicit expired-cursor/resync behavior before bounded replay pruning |
| Beacon incidents | Open/acknowledgement/snooze state; existing age-only deletion is not the new retention policy |
| Webhook deliveries and function executions | Pending/replay work and unresolved outcomes; historical payload retrieval before offloading |
| Access devices/tokens/grants | Revocation and credential ancestry; token lineage participates in stable operation identity |
| Enrollments, challenges and assertion uses | Validity/replay margins and dependent authorization evidence; independently verify expiry/skew rules |
| Exec sessions | Active leases, authorization links and unresolved completion; restrict archived security metadata |
| Mutation audit events/incidents | Existing 365-day policy, acceptance-linked receipts and unresolved incidents; preserve original signature bytes |
| Recovery drills | Active work and latest qualifying proof used by production readiness |
| Observation buckets | Audit decision readers and referenced evidence before treating these solely as expiring metrics |
| Authority/schema, cron state and notification configuration | Current authoritative state, not diagnostic history; credentials do not belong in general archive bundles |

## Implementation order and completion boundary

1. Define immutable manifest/bundle types and retention holds. Include source
   identities, dependency links, byte counts, checksums, original signed bytes
   and an explicit final evidence cutoff.
2. Add a shared `PutImmutable/Get/Verify` storage contract. The Mini adapter
   needs private files, atomic no-replace publication, directory durability,
   verified duplicate-content behavior and bounded capacity. Fleet requires
   immutable object publication and independent restricted credentials.
3. Persist archive intent with the eligible domain transition; an outbox must
   not mark an incomplete graph sealed simply because its operation finished.
   Upload, read back, verify, then acknowledge exact object identity/checksums.
4. First run in shadow mode with retrieval comparisons and no deletion. This is
   only an intermediate review unit, not delivery of the requested reduced
   control-store retention.
5. Implement archive-aware authorized reads, restore/reindex, reference closure,
   replay-expiry behavior and verified bounded pruning. Demonstrate bounded hot
   state under workload before completing M2. Archive outage retains evidence;
   exhausted evidence reserve must not silently discard audited mutations.

`v2/api/storage/s3.go` provides reusable MinIO-compatible transport. Its current
`PutObject` permits replacement and does not verify stored contents, so it is
not itself the immutable archive contract. `GetObject` uses a private temporary
file, file sync and atomic no-replace hard-link publication; reuse that pattern
with the additional durability and duplicate verification requirements above.
Do not reuse application provisioning or Garage administration credentials.

Required tests include upload/verification/acknowledgement crash boundaries,
duplicate publication, corruption, outages, late saga events, exact signed-byte
retrieval, unauthorized historical reads, replay-expired clients, restore without
a mandatory historical PG database, and retention holds for rollback/Fleet/token
lineage. Local storage tests alone do not qualify Fleet object semantics.
