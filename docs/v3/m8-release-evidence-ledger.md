# M8 release evidence ledger

This ledger is a release checklist, not a qualification result. The M8 owner
sign-off requires evidence for the same immutable candidate across all lanes.
Record a protected run URL, artifact digest, start/end time, and reviewer for
each completed row. A source test or signed bundle alone does not pass a live
Fleet or Mini gate.

## Candidate identity

| Component | Required pin and verification |
| --- | --- |
| Norn API, CLI and web UI | Full source SHA, signed bundle version and digest, signing key ID, installed binary/UI digests on Mini and every Fleet control node |
| Norn Fleet | Full protected `main` SHA, controller/executor source digest, protected workflow SHA, installed job-start gate digest and signed operation receipt |
| NornUI | Full source SHA, packaged artifact digest, actual client version and capability response from each supported endpoint |
| Upstream | Exact etcd, Nomad, Consul, ingress, OS/kernel and database engine versions per node; supported skew during each rolling upgrade |
| Fixture | Workload source and image digests, database schema/fixture hash, writer and schedule identities, topology, resource reservations and traffic profile |

Freeze these pins before the first soak interval. A later source or binary
change starts a new qualification candidate; attach prior evidence only if an
owner records why the changed component cannot affect that lane.

The existing work-staging Fleet can supply the three-member topology and
directly deployed canary fixture after fresh protected readback. Its previous
reconciliation receipt is a starting-state proof, not the beginning of the
72-hour interval. Start the clock only when a protected controller records an
immutable candidate binding and first valid telemetry sample. The controller
must sample all three etcd members and the public workload throughout the
interval, preserve gaps and alarms as failures, and publish a terminal receipt
with raw sample hashes and elapsed time. The 10× burst needs a separately
bounded load profile and measured baseline; nominal request counts do not
establish a tenfold rate. A workflow that merely sleeps for 72 hours cannot
qualify this gate.

## Required evidence

| Gate | Protected evidence and pass condition | Status |
| --- | --- | --- |
| Client and capability matrix | CLI, web UI, NornUI and supported operator paths against Mini PG and Fleet etcd; unsupported routes remain closed and advertised capabilities match successful operations | Open |
| Operator access | Second operator granted, connected, read status, performed a scoped authorized operation, then revoked; audit and denied post-revocation attempt read back | Open |
| Fleet reconciliation | One operation per generation with exact plan/attempt/receipt lineage; no duplicate external effects across crash, uncertain response, lease loss, partition and stale executor replay | Open |
| Availability | Representative load and synthetic requests through planned rollout, active API loss, app allocation replacement and loaded 2→3→2 capacity cycle; report request counts, errors, p50/p95/p99 and mutation pause separately | Open |
| Etcd soak | 72 continuous hours at representative operation/event/watch load and a measured 10× burst; record quota, logical and physical growth, compaction/defrag, watch lag, disk latency, alarms and peak resources | Open |
| Etcd faults and restore | One voter lost, quorum lost, full disk/quota alarm, stale executor, corrupted snapshot, archive outage, and clean-host snapshot restore without control PG; prove fencing and compare acknowledged operation/effect IDs | Open |
| Running upgrades | Norn A→B, independently staged upstream one-voter/node upgrades, representative app upgrade and interrupted-operation recovery; record actual traffic and API/app interruption | Open |
| Mini rehearsal | Private copied-state upgrade, rollback or forward-only legacy fence, IDs/credentials/receipts/schedules/routes preserved, backup retrieval and restore | Open |
| Application runtime | Direct Fleet database-backed web/worker deployment, database restore, schedule uniqueness, node replacement, release rollback or forward repair, and acknowledged write reconciliation | Open |
| Runbooks and sign-off | Exact commands, stop/rollback boundaries, recovery receipts, owner approvals and deferred capability list attached to the candidate | Open |

The draft numerical targets are in [planning contracts](planning-contracts.md#measurement-and-qualification-plan).
They are not established SLAs. A failed target needs a recorded scope/design
decision; do not weaken the threshold inside a result bundle.

## Evidence boundary

The Fleet `pre-release-pilot-qualification` workflow is a bounded no-cloud
source/package check. The Norn platform release workflow publishes signed
artifacts. Neither supplies the 72-hour soak, provider-backed fault/restore,
loaded traffic, or operator proof in this ledger. M9 deployment and Mini
cutover remain separate from M8 candidate qualification.
