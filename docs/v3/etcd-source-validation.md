# Etcd source-validation runtime

`NORN_CONTROL_BACKEND=etcd` remains refused by the ordinary API and host-agent
runtimes. `norn-api` can start the narrow source-validation runtime only when
all of the following are configured:

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
| `GET /api/health` | Checks configured etcd members and returns `503 source_validation_unavailable` if none answer. |
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
