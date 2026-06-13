# Beacon Events

Beacon is Norn's operational event surface. It records events Norn can observe
directly, broadcasts them over the existing WebSocket hub, and can forward them
to an external sink such as Vigil.

Beacon is intentionally not a push-notification service. Norn emits trusted
infrastructure events; downstream systems decide whether those events become
incidents, notifications, or app timelines.

## Endpoints

```http
GET  /api/events
GET  /api/events/{id}
POST /api/events
POST /api/events/{id}/ack
POST /api/events/{id}/snooze
POST /api/events/{id}/open
GET  /api/events/sinks
POST /api/events/test
GET  /api/alerts/rules
```

`GET /api/events` accepts optional filters:

| Query | Purpose |
| --- | --- |
| `app` | Filter by app id |
| `type` | Filter by event type |
| `severity` | Filter by `info`, `warning`, or `critical` |
| `limit` | Page size, capped at 200 |
| `offset` | Offset for pagination |

`POST /api/events/test` emits a manual `beacon.test` event. It accepts an
optional body:

```json
{
  "app": "field-harbor"
}
```

## Event Shape

```json
{
  "id": "evt_...",
  "source": "norn",
  "app": "field-harbor",
  "environment": "mini",
  "type": "deploy.failed",
  "severity": "critical",
  "state": "open",
  "title": "field-harbor deploy failed",
  "body": "Deploy failed at healthy: service did not become healthy.",
  "dedupeKey": "field-harbor:deploy",
  "occurredAt": "2026-06-08T09:15:00Z",
  "metadata": {
    "deploymentId": "...",
    "sagaId": "...",
    "step": "healthy"
  }
}
```

## Operator State

Beacon events can be acknowledged, snoozed, and reopened without mutating the
original event body or metadata.

```bash
norn events
norn events show <event-id>
norn events ack <event-id> --note "investigating"
norn events snooze <event-id> --for 2h
norn events open <event-id>
```

Snoozes are time-bound. Once `snoozedUntil` is in the past, the event reads as
`open` again. Acknowledgements record operator and note fields, but never store
secret values.

`GET /api/alerts/rules` returns Norn's built-in event-to-alert catalogue for
deploy failures, service health, cron failures, and recovery events. It is a
shared contract for the CLI, dashboard, and downstream sinks; it is not a
separate paging engine.

## Built-In Events

Deploys emit:

| Type | Severity | When |
| --- | --- | --- |
| `deploy.succeeded` | `info` | A deployment completes |
| `deploy.failed` | `critical` | A deployment fails at a pipeline step |

Rollbacks emit:

| Type | Severity | When |
| --- | --- | --- |
| `rollback.succeeded` | `info` | A rollback completes |
| `rollback.failed` | `critical` | A rollback fails |

Cron control actions emit:

| Type | Severity | When |
| --- | --- | --- |
| `job.triggered` | `info` | An operator manually triggers a periodic process |
| `job.paused` | `warning` | An operator pauses a periodic process |
| `job.resumed` | `info` | An operator resumes a periodic process |
| `job.schedule_updated` | `info` | An operator changes a periodic process schedule |

The v2 runtime uses Nomad periodic jobs for scheduled work. The Nomad watcher
emits cron outcome events:

| Type | Severity | When |
| --- | --- | --- |
| `cron.succeeded` | `info` | A periodic child run completed |
| `cron.failed` | `critical` | A periodic child allocation failed |
| `cron.lost` | `critical` | A periodic child allocation was lost |
| `cron.hung` | `critical` | A periodic child appears stuck beyond the watcher threshold |

Service health transitions emit:

| Type | Severity | When |
| --- | --- | --- |
| `service.health.warning` | `warning` | Consul health changes to warning |
| `service.health.critical` | `critical` | Consul health changes to critical |
| `service.health.recovered` | `info` | A previously non-passing service returns to passing |

Snapshot operations emit:

| Type | Severity | When |
| --- | --- | --- |
| `snapshot.restored` | `warning` | An operator restores a local database snapshot |
| `snapshot.retention.applied` | `info` | Snapshot retention is applied and older local snapshot files are pruned |

## Sink Configuration

Beacon sink delivery is configured by environment variables:

```bash
NORN_BEACON_ENVIRONMENT=mini
NORN_BEACON_SINK_URL=https://vigil.example.com/api/events
NORN_BEACON_SINK_KEY_ID=norn-mini
NORN_BEACON_SINK_SECRET=...
```

Sink requests include:

```http
X-Beacon-Source: norn
X-Vigil-Key-Id: norn-mini
X-Vigil-Timestamp: 2026-06-08T09:15:00Z
X-Vigil-Signature: <hmac-sha256 hex>
```

The signature input is:

```text
<timestamp>
<raw JSON body>
```

Keep sink credentials in the service runtime environment or secret manager.
They should not be written into app repositories.
