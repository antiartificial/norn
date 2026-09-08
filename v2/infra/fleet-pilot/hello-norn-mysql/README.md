# Hello Norn MySQL

Small synthetic workload for the cloud staging pilot. The Mini remains an
independent development host. This app stores synthetic request IDs only.

The checked-in InfraSpec is a disabled catalog template. Its non-routable
repository, registry and HTTPS values must be replaced in a reviewed,
root-owned read-only Norn app catalog with a private repository, a signed OCI
manifest digest and staging hostname before enabling it. This matches the
post-bootstrap `NORN_APPS_DIR` model: Fleet supplies Nomad/Consul clients,
while Norn consumes the reviewed catalog and submits the workload. The
included `nomad/hello-norn-mysql.nomad.hcl` is an equivalent direct-Nomad
bootstrap/rehearsal job; do not run both paths at once.

Assign two replicas to distinct clients in the `ingress` Nomad pool. A
one-allocation canary alongside those two replicas requires a third eligible
client; use a rolling release on the fixed two-client topology.

`MYSQL_DSN` uses Go MySQL driver syntax, such as
`pilot:password@tcp(database.example:25060)/pilot`. Supply it through the
encrypted catalog. The process always enforces certificate-verified TLS,
bounded connection timeouts and an eight-connection pool. An optional
`MYSQL_CA_FILE` selects a mounted provider CA; it must be available on every
allocation. Never use `skip-verify` or publicly allowlist the database.

Run `/hello-norn-mysql migrate` once using the migration database identity.
This creates only `pilot_records`. The runtime identity needs SELECT, INSERT
and UPDATE on that table. Norn's PostgreSQL snapshot path does not protect
MySQL: retain provider backup/restore evidence before schema changes.

For a direct-Nomad rehearsal after `norn-fleet` bootstrap, put the two runtime
values in the ACL-restricted `nomad/jobs/hello-norn-mysql` Nomad variable path,
then submit the direct job with a reviewed digest only:

```sh
nomad job run -var 'image=REGISTRY/hello-norn-mysql@sha256:…' nomad/hello-norn-mysql.nomad.hcl
```

The HCL deliberately has no MySQL server resource: the database remains a
provider-managed dependency, and this job uses only the bootstrap-created
Nomad and Consul substrate. Its dynamic service port is discovered by Consul;
publish the HTTPS route using the reviewed staging ingress configuration.

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
IDs; never DSNs, credentials, CA material or response bodies). A nonzero exit
identifies availability below the selected threshold or unverified writes.

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
