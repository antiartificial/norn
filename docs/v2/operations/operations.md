# Operations Ledger

Norn records long-running and externally triggered work in a durable PostgreSQL operations ledger. The ledger is separate from saga events: saga events are the detailed timeline, while operations are the compact status index used for drain checks, dashboards, metrics, and CLI summaries.

## Surfaces

```bash
norn operations
norn operations --active
norn operations <operation-id>
norn events
norn events show <event-id>
norn events ack <event-id>
norn alerts
norn smoke platform
norn platform smoke
norn webhooks
norn webhooks replay <delivery-id>
norn webhooks replay <delivery-id> --preflight
norn ops platform
norn observability bundle
norn observability install
norn secrets migrate-plan
norn network
```

API endpoints:

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/operations` | Recent operations, filterable by app, kind, status, and active state |
| `GET` | `/api/operations/active` | Queued/running operations for drain gates |
| `GET` | `/api/v1/operations/{id}` | Authoritative status and receipt for one durable operation |
| `GET` | `/api/v1/capabilities` | Protocol version, supported features, endpoints, and token scopes |
| `POST` | `/api/v1/platform/preflights` | Queue a candidate platform build and health check |
| `POST` | `/api/v1/platform/upgrades` | Queue a restart-safe platform upgrade |
| `POST` | `/api/v1/platform/smoke` | Queue authenticated platform smoke |
| `POST` | `/api/v1/host/assurances` | Queue host assurance |
| `GET`, `POST` | `/api/v1/apps/{id}/snapshots` | List snapshots or queue a manual snapshot |
| `POST` | `/api/v1/apps/{id}/snapshots/retention` | Queue reviewed newest-N pruning |
| `POST` | `/api/v1/apps/{id}/snapshots/{snapshot}/restore` | Queue an exact inventory-filename restore with mandatory safety snapshot; unique legacy timestamps remain compatible |
| `POST` | `/api/v1/apps/{id}/migrations` | Queue the declared schema migration independently of deploy |
| `POST` | `/api/v1/apps/{id}/rollbacks` | Queue rollback to the previous successful deployment |
| `WS` | `/api/v1/events?after=<cursor>` | Authenticated durable control events with cursor replay |
| `GET` | `/api/deployments/{id}/steps` | Durable deploy or rollback stage checkpoints |
| `GET` | `/api/events` | Recent Beacon events, filterable by app, type, and severity |
| `GET` | `/api/events/{id}` | Beacon event detail with operator state and metadata |
| `POST` | `/api/events/{id}/ack` | Acknowledge a Beacon event |
| `POST` | `/api/events/{id}/snooze` | Snooze a Beacon event until a duration or timestamp |
| `POST` | `/api/events/{id}/open` | Reopen a Beacon event |
| `GET` | `/api/alerts/rules` | Built-in alert rule catalogue derived from Beacon event types |
| `GET` | `/api/observability/bundle` | Prometheus, alert, Grafana, and service bundle |
| `GET` | `/api/observability/alerts.yml` | Prometheus alert rules from the observability bundle |
| `POST` | `/api/observability/services/install` | Install generated observability app directories |
| `GET` | `/api/access/patterns` | Hosted-service access pattern rollups and idle candidate hints |
| `POST` | `/api/access/observations` | Record aggregate hosted-service access observations |
| `GET` | `/api/tuning/recommendations` | Advisory resource tuning recommendations from live signals |
| `GET` | `/api/secrets/migration-plan` | Value-safe plaintext secret migration plan |
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
| `app.snapshot` | web, macOS, or v1 API | database snapshot |
| `app.snapshot-prune` | web, macOS, or v1 API | destructive snapshot retention |
| `app.snapshot-restore` | web, macOS, or v1 API | destructive restore with safety snapshot |
| `app.migrate` | web, macOS, or v1 API | schema mutation with pre-migration snapshot |
| `platform.preflight` | v1 control API / `platform queue-preflight` | read-only candidate build |
| `platform.upgrade` | v1 control API / `platform queue-upgrade` | control-plane replacement |
| `platform.rollback` | v1 control API / `platform queue-rollback` | control-plane replacement |
| `platform.smoke` | v1 control API / `platform queue-smoke` | read-only platform assurance |
| `host.assure` | v1 control API / `host queue-assure` | bounded host repair and endpoint probes |

App preflights, deploys, rollbacks, snapshots, pruning, restores, and standalone migrations are queued in the operations table and claimed by the API worker with `FOR UPDATE SKIP LOCKED`. A PostgreSQL advisory lock serializes all work for one app across API replicas. Queue rows include payload, attempt count, max attempts, lease owner, lease expiry, next attempt, and last error.

Configure at least four PostgreSQL pool connections per Norn API replica. The
API validates this at startup because one worker may simultaneously hold its
app advisory lock, execute pipeline SQL, renew its lease, and serve
control/recovery traffic.

Platform and host operations are claimed by `norn-host-agent`, an independent
process installed as `com.norn.host-agent`. The API process never executes
arbitrary shell input: the agent maps the five known operation kinds to fixed
script subcommands, validates refs and modes again, renews its lease, and stores
a bounded output receipt. Because the agent is not the API process, it remains
alive while an upgrade restarts Norn.

Deploy and rollback stages are written to `deployment_steps`. Read-only preflights can retry safely. App deploys are queued and visible to drain gates; after an API restart, a running deploy can be requeued only if no mutable stage checkpoint has started. If interruption happens during or after snapshot, migration, submit, health, forge, or cleanup, the operation fails visibly for manual review rather than replaying side effects blindly.

Operators can inspect checkpoint evidence with:

```bash
norn deploy steps <deployment-id>
```

This is the supported pre-resume surface. Automatic mutable-stage resume remains intentionally disabled until each mutable stage records enough receipt data for safe operator confirmation.

## Event Operations

Beacon events now carry operator state:

| State | Meaning |
|-------|---------|
| `open` | Event is active or a previous snooze has expired |
| `snoozed` | Event is hidden from immediate attention until `snoozedUntil` |
| `acknowledged` | An operator has accepted/handled the event |

Acknowledgement and snooze metadata is stored beside the immutable event payload. The Platform tab and `norn events` expose the same state. Related saga, deployment, operation, service, process, and cron ids are carried through event metadata when available.

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

Direct `norn platform upgrade` and rollback commands still call the local
platform script. The preferred remote/operator path is `norn platform
queue-upgrade`, which creates a durable operation and waits for the independent
host agent. The script excludes that operation's own ID from the drain query,
so it still blocks on unrelated app or maintenance work without deadlocking on
itself.

`norn platform upgrade --proxy` uses the same drain gate before switching the managed reverse-proxy upstream on hosts that are intentionally proxy-fronted.

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
| `norn_service_status` | Live service status by app/process/service/status |
| `norn_snapshot_over_limit_total` | Snapshot retention pressure |
| `norn_beacon_events_total` | Beacon events by type and severity |
| `norn_beacon_last_occurred_timestamp_seconds` | Last Beacon event time by type and severity |
| `norn_host_disk_free_bytes` | Host disk free space visible to the API process |

## Platform Rollup

`norn ops platform` includes assurance fields alongside the existing service, deployment, access, and release summaries:

| Field | Meaning |
|-------|---------|
| `secrets.migrationItems` | Count of plaintext secret-like env keys that have a migration plan action |
| `observability.bundleAvailable` | Whether the API can generate the local observability bundle |
| `observability.retention` | Recommended local Prometheus retention target |

## Next Step

Add deeper stage-level resume data for mutable app stages before enabling
automatic retries after snapshot, migration, submit, or route changes.
