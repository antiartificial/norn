# Hello Norn MySQL

Small synthetic workload for the cloud staging pilot. The Mini remains an
independent development host. This app stores synthetic request IDs only.

The checked-in InfraSpec is deliberately `deploy: false`. Norn's current
translator injects encrypted catalog secrets into `task.Env`; that is not an
acceptable MySQL-password transport. Use the direct Nomad jobs below until the
translator supports ACL-restricted Nomad-variable templates and secret files.
The catalog remains an immutable artifact/placement reference only—do not
enable it or add `MYSQL_DSN` to `secrets.enc.yaml` for this pilot.

Assign two replicas to distinct clients in the `ingress` Nomad pool. A
one-allocation canary alongside those two replicas requires a third eligible
client; use a rolling release on the fixed two-client topology.

`MYSQL_DSN` uses Go MySQL driver syntax, such as
`pilot:password@tcp(database.example:25060)/pilot`. It is rendered from an
ACL-restricted Nomad runtime variable into the allocation only. The process
requires `MYSQL_CA_FILE`; it trusts only that mounted provider CA, verifies the
database hostname and TLS 1.2+, and uses bounded connection timeouts with an
eight-connection pool. Never use `skip-verify`, the system trust pool, or a
public database allowlist.

Run `/hello-norn-mysql migrate` once using the migration database identity.
This creates only `pilot_records`. The runtime identity needs SELECT, INSERT
and UPDATE on that table. Norn's PostgreSQL snapshot path does not protect
MySQL: retain provider backup/restore evidence before schema changes.

For a direct-Nomad rehearsal after `norn-fleet` bootstrap, a protected
bootstrap/migration procedure must create three separate variable paths:

- `nomad/pilot/hello-norn-mysql/provider-ca`: reviewed provider CA PEM only;
- `nomad/jobs/hello-norn-mysql/runtime`: runtime `MYSQL_DSN` with only
  `SELECT`, `INSERT`, and `UPDATE` on `pilot_records`;
- `nomad/jobs/hello-norn-mysql/migration`: one-time `MYSQL_DSN` whose identity
  can create `pilot_records`, plus the runtime table privileges.

Nomad ACL policies must give the web job only `runtime` plus `provider-ca`, and
the migration job only `migration` plus `provider-ca`. The direct job renders
the DSN through a quoted dotenv template and mounts the multiline CA as
`secrets/mysql-ca.pem` with mode `0400`; neither value is a task environment
field or catalog secret. Submit only a reviewed digest, source SHA and exact
pilot hostname:

```sh
nomad job run \
  -var 'image=REGISTRY/hello-norn-mysql@sha256:…' \
  -var 'source_version=EXACT_SOURCE_SHA' \
  -var 'hostname=pilot.example.com' \
  nomad/hello-norn-mysql.nomad.hcl
```

The HCL deliberately has no MySQL server resource: the database remains a
provider-managed dependency, and this job uses only the bootstrap-created
Nomad and Consul substrate. The DigitalOcean load balancer passes TLS through
on `:443` to Traefik's `websecure :443` entrypoint. The explicit router enables
only this exact host's `/records/*` and `/version` routes; readiness stays
private. Consul requires `/readyz`, while Nomad restarts a failed `/healthz`
liveness check without restarting merely for a database readiness failure.

Run the migration once before the web job, with the same reviewed image:

```sh
nomad job run -var 'image=REGISTRY/hello-norn-mysql@sha256:…' \
  nomad/hello-norn-mysql-migrate.nomad.hcl
```

Retain the successful allocation receipt, then revoke and remove the
`migration` variable and its ACL policy binding. Do not retain a cluster-admin
DSN: the Fleet-owned bootstrap must derive/create these two least-privilege
database identities and write only their scoped DSNs to the matching Nomad
variables. On pilot retirement, stop the web job first, then remove the
runtime variable, provider-CA variable, and their scoped ACL bindings.

Build with reviewed, digest-pinned `GO_IMAGE` and `RUNTIME_IMAGE` build args
and the exact app source SHA as `APP_VERSION`. Run `go test ./...` first.

The workload provides:

- `/healthz`: process liveness independent of database readiness.
- `/readyz`: verified database connectivity; setting
  `PILOT_FAIL_READINESS=true` in a reviewed test release makes it fail.
- `/version`: source version and allocation identity with caching disabled.
- `PUT /records/{id}`: idempotent synthetic write, followed by readback.
- `GET /records/{id}`: read a known synthetic ID.
- `/metrics`: bounded request and failure counters, without per-ID labels.

Restrict synthetic write access to the reviewed load generator through the
gateway. Keep metrics private. SIGTERM drains HTTP requests for up to 15 seconds;
the InfraSpec allows 20 seconds before forced termination.

From outside the fleet, run the bounded probe and retain its JSON stdout:

```sh
python3 ../exercise.py --url https://YOUR-STAGING-HOST --rps 5 --seconds 60
```

The probe uses at most 16 workers, 50 offered requests/second and ten minutes.
It writes and rereads a baseline record, aborts after 20% failures in the
latest 50 requests, proves every acknowledged write after the run, and emits
only value-safe JSON (synthetic receipt IDs, counts, timings and allocation
IDs; never DSNs, credentials, CA material or response bodies). Evidence binds
the tested origin and requested load to UTC start/end timestamps, observed
source version, pinned job image metadata, and sanitized durable Nomad
allocation inspections. Run it from a Nomad-ACL-authorized operator runner.
A nonzero exit identifies availability below the selected threshold, missing
Nomad evidence, or unverified writes.

For the optional, allocation-only recovery rehearsal, select one current app
allocation explicitly and run:

```sh
python3 ../exercise.py --url https://YOUR-STAGING-HOST --fault-allocation ALLOCATION_ID
```

The harness first verifies through Nomad that the ID belongs to this job, then
uses `nomad alloc stop` on that allocation only and waits for the two-replica
service to recover through the public origin. It never calls a managed MySQL
failover, changes database configuration, or runs provider commands. Use one
fault at a time and capture gateway, Nomad, Consul, host and database metrics
alongside the JSON evidence.

Removing app allocations does not delete the database or cloud resources.
Retire only the explicitly owned rehearsal resources through reviewed Fleet
retirement. Persistent staging remains online until explicitly retired.
