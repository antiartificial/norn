# M7 Mini application candidate review — 2026-09-26

Status: read-only candidate inventory, not a migration selection or M7 sign-off.
The authenticated Mini API inventory was collected at 2026-09-27 01:36 UTC.
The raw responses are held only in a private local work directory; this note
records non-secret workload shape. No Mini or Fleet mutation was made.

The Mini still reports v2.20.0-platform-30-ga5da8ef, 27 app records, zero
active operations, 44 service entries, and no configured Fleet node pools.
Those are point-in-time observations; they do not prove an upgrade drain or a
destination exists.

| Candidate | Relevant live shape | Why it is not yet an approved first cutover |
| --- | --- | --- |
| `field-harbor` | Healthy web allocation; declared PostgreSQL, one volume, and three scheduled processes | Covers database, files, and scheduled work, but the API reports snapshot retention over limit. Source volume ownership, job acknowledgments, data size, ingress, and destination bindings still need a protected inventory. |
| `turnkey-offer-intake` | Healthy web and singleton worker allocations; declared PostgreSQL and object storage | Covers database, object files, and worker handoff, but the API also reports snapshot retention over limit. Public traffic and worker ownership raise the cost of a first cutover; source/destination storage and rollback semantics are unproved. |
| `contextdb` | Healthy web and review-worker allocations | The API summary does not declare an infrastructure database or file store for this app, so it cannot yet prove the required database-and-files mobility scope. |

First qualify the full journal with a disposable **Mini-hosted representative
fixture** containing PostgreSQL rows, file objects, web requests, an
acknowledged work queue, and a schedule. The fixture must really run on Mini
and move to an independent Fleet; a local-only simulation is insufficient.
The deploy-disabled [mobility fixture](https://github.com/antiartificial/norn/blob/master/v2/infra/mobility-fixture/README.md)
now provides those five shapes and reports item digests, missing/mismatched/
orphaned files, pending/acknowledged jobs, and schedule ticks. Its web, worker,
and schedule writers all require explicit admission. A disposable PostgreSQL
16 integration test passed locally for writes, acknowledgment, tick, inventory
drift, and writer fencing; the Norn CI API job also runs this test. No Mini or
Fleet deployment, traffic cutover, provider restore, or migration-journal
rehearsal has been performed with it.
Then select a real app with an owner-approved consistency group and rehearse
its exact source and target providers. Do not use the fixture result to claim
that either listed app's data engine, object/volume path, traffic, or worker
semantics is supported.

The M7 gate still requires a protected Fleet, source and target identity
mapping, writer and schedule fencing, data/file/work reconciliation, traffic
cutover, rollback before target writes, and forward recovery after target
writes. This candidate review changes none of those requirements.
