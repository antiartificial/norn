# Etcd source-validation runtime

`norn-api` has two deliberately bounded etcd runtimes. This page describes the
development-only source-validation runtime, selected when all of the following
are configured:

- `NORN_ETCD_SOURCE_VALIDATION=true`
- `NORN_ETCD_ENDPOINTS` and `NORN_ETCD_PREFIX`
- a UUID `NORN_CONTROL_AUTHORITY`
- `NORN_REQUIRE_EXPLICIT_AUTH=true`
- an API token and audit signing key of at least 32 bytes

The mode is PG-free. It selects etcd before any call to `store.Connect`, uses
the etcd `AuthStore` for managed-token verification and revocation, and uses
the v3 etcd operation/checkpoint store for signed acceptance and execution.
The raw shared API secret is never accepted as a request credential.

## Supported surface

| Endpoint | Behavior |
| --- | --- |
| `GET /api/health` | Performs a bounded linearizable etcd read at the configured control prefix and returns `503 source_validation_unavailable` when that quorum-backed path is unavailable. |
| `GET /api/version` | Returns the API version. |
| `GET /api/v1/capabilities` | Advertises the bounded source-validation capability set. |
| `POST /api/v1/source-validation/apps/{id}/preflights` | Requires a managed etcd token with `api:write` and an idempotency key. Accepts only source-only preflight. |
| `GET /api/v1/source-validation/operations/{id}` | Requires a managed etcd token with `api:read` or `api:write`; returns the operation terminal state. |

Every other API route returns `503 source_validation_route_unsupported` before
it can invoke a PostgreSQL handler or external effect.

## Limits and exclusions

The worker claims only `app.preflight`. The accepted `InfraSpec` must have no
`build` section and cannot use production admission. Source validation records
the signed request, an etcd generation-fenced operation claim, an immutable
source checkpoint, and a terminal operation result. It does not run Docker,
tests, deploys, snapshots, migrations, Nomad, Consul, recovery, effects,
archive retention, or any general API aggregate.

This is a development source-validation mode. It does not establish Fleet
bootstrap, TLS/member lifecycle, three-member quorum, recovery, backup/restore,
fault, or soak qualification.

## Normal Fleet runtime bootstrap

The normal etcd runtime serves only signed Fleet capacity-plan receipts. In
production it requires HTTPS endpoints, an explicit CA bundle, client
certificate/key, and an etcd username/password (`NORN_ETCD_CA_FILE`,
`NORN_ETCD_CERT_FILE`, `NORN_ETCD_KEY_FILE`, `NORN_ETCD_USERNAME`, and
`NORN_ETCD_PASSWORD`).

Before the first API process, an operator can create one initial revocable
managed credential against an empty Norn prefix with:

```text
norn-api --norn-etcd-bootstrap
```

It requires `NORN_ETCD_BOOTSTRAP_TOKEN_FILE` (an absolute new path),
`NORN_ETCD_BOOTSTRAP_SUBJECT`, `NORN_ETCD_BOOTSTRAP_SCOPES`, and
`NORN_ETCD_BOOTSTRAP_TTL` (at most 72 hours). The command reserves the empty
prefix, records the managed token, and writes the opaque token only to the new
owner-only file. It neither prints the token nor runs a server. A second run or
a non-empty prefix is refused.

`NORN_STARTUP_MODE=passive` with `NORN_SCHEMA_MODE=check` serves only the
loopback `/api/health`, `/api/version`, and `/api/schema` status routes.
`NORN_SCHEMA_MODE=migrate-only` verifies the etcd schema/read path and exits
without serving or writing records.
