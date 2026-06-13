# Operations Ledger

Norn records long-running and externally triggered work in a durable PostgreSQL operations ledger. The ledger is separate from saga events: saga events are the detailed timeline, while operations are the compact status index used for drain checks, dashboards, metrics, and CLI summaries.

## Surfaces

```bash
norn operations
norn operations --active
norn webhooks
norn webhooks replay <delivery-id>
norn webhooks replay <delivery-id> --preflight
norn ops platform
```

API endpoints:

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/operations` | Recent operations, filterable by app, kind, status, and active state |
| `GET` | `/api/operations/active` | Queued/running operations for drain gates |
| `GET` | `/api/webhooks/deliveries` | Recent webhook delivery inbox |
| `POST` | `/api/webhooks/deliveries/{id}/replay` | Replay a delivery as a deploy or preflight |
| `GET` | `/metrics` | Prometheus counters for operations and webhooks |

## Recorded Operations

The current release records these operation kinds:

| Kind | Source | Risk |
|------|--------|------|
| `app.preflight` | `norn preflight` / API preflight | read-only |
| `app.deploy` | `norn deploy`, webhook auto-deploy, API deploy | app rolling update |
| `app.rollback` | `norn rollback` / API rollback | app rolling update |

App preflights and deploys are queued in the operations table and claimed by the API worker with `FOR UPDATE SKIP LOCKED`. Queue rows include payload, attempt count, max attempts, lease owner, lease expiry, next attempt, and last error.

Read-only preflights can retry safely. App deploys are queued and visible to drain gates, but an interruption during mutable deploy execution is marked failed rather than replaying snapshot, migration, or Nomad submit stages blindly.

## Webhook Inbox

Webhook deliveries are recorded before validation decisions. The inbox captures provider, event type, delivery id, repository, ref, matched app, saga id, final status, and ignored/failed reason. This makes webhook auto-deploy behavior inspectable without scraping API logs.

Delivery statuses include:

| Status | Meaning |
|--------|---------|
| `received` | Delivery row was created |
| `ignored` | Valid delivery, but Norn intentionally ignored it |
| `failed` | Validation, parsing, or discovery failed |
| `deploying` | Delivery matched an app and queued an app deploy |
| `replayed` | Operator replayed the delivery as a deploy or preflight |

Replay uses the normal authenticated API path:

```bash
norn webhooks replay <delivery-id>
norn webhooks replay <delivery-id> --preflight
```

## Platform Drains

`norn platform upgrade` and `norn platform rollback` call the platform script. When `NORN_API_TOKEN` or `NORN_TOKEN` is available, the script checks `/api/operations/active` before mutating the running platform.

Set `NORN_DRAIN_MODE` to choose behavior:

| Mode | Behavior |
|------|----------|
| `fail` | Default. Refuse to upgrade while operations are active |
| `wait` | Poll until active operations finish |
| `force` | Skip the drain check |

If the current API is too old to expose the operations endpoint, or auth is unavailable, the script logs a warning and continues. This keeps bootstrap upgrades possible.

## Metrics

The metrics endpoint exports:

| Metric | Meaning |
|--------|---------|
| `norn_operations_total` | Operation count by kind and status |
| `norn_operation_duration_seconds_count` | Completed operation duration sample count |
| `norn_operation_duration_seconds_sum` | Total completed operation duration |
| `norn_operation_last_started_timestamp_seconds` | Last operation start time |
| `norn_webhook_deliveries_total` | Webhook delivery count by provider and status |
| `norn_webhook_last_received_timestamp_seconds` | Last webhook delivery time |

## Next Step

Add stage-level deploy resume checkpoints before enabling automatic retries for interrupted mutable deploys. Move app rollback and platform preflight/upgrade jobs into the same durable worker lane after that checkpoint model exists.
