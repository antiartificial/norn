# Hello Norn MySQL

Small synthetic workload for the cloud staging pilot. The Mini remains an
independent development host. This app stores synthetic request IDs only.

The checked-in InfraSpec is disabled. Bind a private repository, a reviewed
signed image digest and the staging HTTPS hostname before enabling it. Assign
two replicas to distinct clients in the `ingress` Nomad pool. A one-allocation
canary alongside those two replicas requires a third eligible client; use a
rolling release on the fixed two-client topology.

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

Build with reviewed, digest-pinned `GO_IMAGE` and `RUNTIME_IMAGE` build args
and the exact app source SHA as `APP_VERSION`. Run `go test ./...` first.

The workload provides:

- `/health/live`: process liveness independent of database readiness.
- `/health/ready`: verified database connectivity; setting
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
It aborts after 20% failures in the latest 50 requests and rereads every
acknowledged write. A nonzero exit identifies request errors or unverified
writes; rerun read verification after recovery before claiming data loss.
Use separate runs for baseline, rolling deployment and one fault at a time.
Capture gateway, Nomad, Consul, host and database metrics alongside probe output.

Removing app allocations does not delete the database or cloud resources.
Retire only the explicitly owned rehearsal resources through reviewed Fleet
retirement. Persistent staging remains online until explicitly retired.
