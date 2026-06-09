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
POST /api/events
GET  /api/events/sinks
POST /api/events/test
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

## Built-In Events

Deploys emit:

| Type | Severity | When |
| --- | --- | --- |
| `deploy.succeeded` | `info` | A deployment completes |
| `deploy.failed` | `critical` | A deployment fails at a pipeline step |

Cron control actions emit:

| Type | Severity | When |
| --- | --- | --- |
| `job.triggered` | `info` | An operator manually triggers a periodic process |
| `job.paused` | `warning` | An operator pauses a periodic process |
| `job.resumed` | `info` | An operator resumes a periodic process |
| `job.schedule_updated` | `info` | An operator changes a periodic process schedule |

The v2 runtime uses Nomad periodic jobs for scheduled work. Beacon records
operator-level cron actions today. Allocation outcome events such as
`job.succeeded`, `job.failed`, `job.hung`, and `job.missed` belong in the next
Nomad allocation watcher layer.

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
