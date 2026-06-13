# Operations Ledger

Norn records long-running and externally triggered work in a durable PostgreSQL operations ledger. The ledger is separate from saga events: saga events are the detailed timeline, while operations are the compact status index used for drain checks, dashboards, metrics, and CLI summaries.

## Surfaces

```bash
norn operations
norn operations --active
norn events
norn smoke platform
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
| `GET` | `/api/deployments/{id}/steps` | Durable deploy or rollback stage checkpoints |
| `GET` | `/api/events` | Recent Beacon events, filterable by app, type, and severity |
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

App preflights, deploys, and rollbacks are queued in the operations table and claimed by the API worker with `FOR UPDATE SKIP LOCKED`. Queue rows include payload, attempt count, max attempts, lease owner, lease expiry, next attempt, and last error.

Deploy and rollback stages are written to `deployment_steps`. Read-only preflights can retry safely. App deploys are queued and visible to drain gates; after an API restart, a running deploy can be requeued only if no mutable stage checkpoint has started. If interruption happens during or after snapshot, migration, submit, health, forge, or cleanup, the operation fails visibly for manual review rather than replaying side effects blindly.

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

Add deeper stage-level resume data for mutable stages before enabling automatic retries after snapshot, migration, submit, or route changes. Move platform preflight/upgrade jobs into the same durable worker lane once platform-scoped operations are modeled.
