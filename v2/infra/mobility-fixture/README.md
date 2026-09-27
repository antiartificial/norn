# V3 mobility fixture

This is a deploy-disabled rehearsal app for M7. It writes PostgreSQL rows and
content-addressed file digests, queues one job per item, acknowledges jobs from
the worker process, and records scheduled ticks. `GET /state` reports rows,
acknowledgments, ticks, missing/mismatched files, and orphaned files. It is
intended to run first on Mini, then on an independent Fleet with separate
database and file storage.

The checked-in InfraSpec has inert source/image locations and
`WRITE_ENABLED=false`. Before either deployment, replace the source and image
with reviewed immutable identities, configure the destination database and
persistent file volume, then explicitly admit writes only for the current
owner. The web, worker, and tick commands all refuse writes when fenced.
Never set `WRITE_ENABLED=true` on source and target at the same time.
Run the explicit `migrate` command before the rehearsal; `serve` never runs
schema changes on startup. Do not run migrations as part of a fenced source.

For a rehearsal, post known bodies to `/items`, run the worker and tick,
and record `/state`, database backups, file inventory, and request results.
Quiesce and fence source writers before final synchronization. Compare every
item ID and digest, acknowledged job, tick, and file count on the target;
exercise rollback before target writes and forward recovery after target
writes. This fixture alone does not prove Norn's migration journal, traffic
switch, provider restore, or M7 acceptance.
