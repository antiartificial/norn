# V3 mobility fixture

This is a deploy-disabled rehearsal app for M7. It writes PostgreSQL rows and
content-addressed file digests, queues one job per item, acknowledges jobs from
the worker process, and records scheduled ticks. `GET /state` reports exact
item, job, and tick identities plus missing/mismatched and orphaned files. It is
intended to run first on Mini, then on an independent Fleet with separate
database and file storage.

The checked-in InfraSpec has inert source/image locations and
`WRITE_ENABLED=false`. Before either deployment, replace the source and image
with reviewed immutable identities, configure the destination database and
persistent file volume, then explicitly admit writes only for the current
owner. The web, worker, and tick commands all refuse writes when fenced.
Never set `WRITE_ENABLED=true` on source and target at the same time.
CI builds the Dockerfile to catch packaging failures; that build is not a
signed or published release image.
Run the explicit `migrate` command before the rehearsal; `serve` never runs
schema changes on startup. Norn's InfraSpec migration command runs on the
control host, so the fixture omits that field and needs a reviewed one-off
`/mobility-fixture migrate` invocation inside its image against the selected
application database before web activation. Do not run migrations as part of
a fenced source. The three regular processes select their mode through
`MOBILITY_MODE` and use the image entrypoint directly; a nonempty InfraSpec
`command` would invoke `/bin/sh`, which the distroless image does not contain.

For a rehearsal, post known bodies to `/items`, run the worker and tick,
and record `/state`, database backups, file inventory, and request results.
Quiesce and fence source writers before final synchronization. Compare every
item ID and digest, acknowledged job, tick, and file count on the target;
`python3 compare_state.py source.json target.json` checks exact quiesced
inventories before target writes and refuses incomplete file inventory or an
observed live writer. Capture state from every serving allocation and prove
old allocations have stopped separately; one HTTP response cannot establish
that no other writer remains. Then
exercise rollback before target writes and forward recovery after target
writes. This fixture alone does not prove Norn's migration journal, traffic
switch, provider restore, or M7 acceptance.
