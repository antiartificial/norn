# M6 first PostgreSQL provider pair review — 2026-09-28

Status: **proposed qualification target, not an approved or provisioned
application database**. No provider mutation or app cutover was performed.

## Current source and destination evidence

- A read-only `SHOW server_version` on the Mini's owner-local Postgres.app
  instance returned **17.7**. The individual application binding and writer
  set for the first cutover have not been selected or verified, so this is a
  host-level version observation, not proof of a particular app's source.
- Fleet draft PR #177's protected `staging/nyc3` root currently instantiates
  the node/ingress module and **no application database**. The reusable
  `modules/managed-databases` module is not active in that root. Its separate
  management root provisions an authority database, which must not be reused
  as an application target.
- The disposable Fleet root declares a PostgreSQL **16** cluster named
  `runtime_postgres`, database `norn_runtime`, with a firewall tag for control
  nodes. That resource is not a selected Mini-to-Fleet application target.
- The DigitalOcean options API, read through `doctl` on 2026-09-28, listed
  Standard PostgreSQL major **17** and region `nyc3`; it also listed
  `db-s-2vcpu-4gb` among two-node PostgreSQL sizes. These are option lists,
  not a confirmed version/region/size combination, price, quota, VPC plan,
  or provisioned cluster. [DigitalOcean's current creation guide](https://docs.digitalocean.com/products/databases/postgresql/how-to/create/)
  also lists PostgreSQL 17 support and directs operators to check live
  options before creating a cluster.

PostgreSQL's [version 17 `pg_dump` documentation](https://www.postgresql.org/docs/17/app-pgdump.html)
does not guarantee loading a dump into an older major server. A PostgreSQL
17 Mini source should therefore **not** be paired with the disposable Fleet
PostgreSQL 16 target for the first restore rehearsal. The selected source
must be checked directly; no app transfer has been attempted.

## Recommended first pair to review

Use the deploy-disabled mobility fixture on a Mini-hosted PostgreSQL 17
application database as the **source** and a separately owned DigitalOcean
managed PostgreSQL 17 application service in `staging/nyc3` as the **target**.
Keep this target separate from Fleet control/management storage and the
disposable `norn_runtime` database. This recommendation preserves a same-major
first transfer while exercising the intended managed application target.

Before any provisioning, a release owner must identify the source binding,
all web/worker/schedule/integration writers and file/queue consistency group;
name the target project, VPC, owner, service size, trusted-source tags,
retention and cost ceiling; and review an exact provider plan. The target
service then needs private TLS, dedicated runtime and maintenance roles,
read-only fence preflight against the active catalog, a disposable provider
fence effect rehearsal, backup/restore, final-sync and crash-recovery proof.
The selected provider must permit the exact delegated role privileges used by
the fence fixture. A live app remains out of scope until the synthetic fixture
passes and its owner approves a separate consistency group.

The M6 gate and **10%** implementation estimate remain open. This review
changes the next qualification target; it does not assert that DigitalOcean
has approved the exact database plan or that any provider role can be fenced.
