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
GET  /api/notifications/channels
POST /api/notifications/channels
POST /api/notifications/channels/{id}/test
DELETE /api/notifications/channels/{id}
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
| `deploy.auto_rollback` | `warning` | A failed health gate queues rollback to the previous successful deployment |
| `canary.promoted` | `info` | An operator manually promotes a canary deployment |

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
| `cron.missed_run` | `critical` | A scheduled process missed its expected dispatch window |

Nomad allocation watcher events emit:

| Type | Severity | When |
| --- | --- | --- |
| `nomad.allocation.failed` | `critical` | An allocation fails |
| `nomad.allocation.lost` | `critical` | Nomad reports an allocation as lost |
| `nomad.task.restarted` | `warning` | A task restart is observed in allocation state |
| `nomad.task.oom_killed` | `critical` | A task was killed by the OOM killer |

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
| `snapshot.exported` | `info` | A local snapshot is exported to remote object storage |
| `snapshot.imported` | `info` | A remote snapshot is imported back into local storage |

## Notification Channels

Beacon can deliver events to configured notification channels in addition to the signed sink. Channels are managed from the CLI, API, or dashboard Platform tab:

```bash
norn notifications list
norn notifications add discord ops https://discord.com/api/webhooks/... --severity critical
norn notifications add ntfy alerts https://ntfy.sh/norn-alerts --severity warning,critical
norn notifications add pushover mobile https://api.pushover.net/1/messages.json \
  --token <app-token> --user-key <user-key> --severity critical
norn notifications add webhook vigil https://vigil.example.com/api/events
norn notifications test <channel-id>
norn notifications remove <channel-id>
```

Providers:

| Provider | Use |
| --- | --- |
| `discord` | Sends color-coded webhook embeds |
| `ntfy` | Sends HTTP posts with priority headers |
| `pushover` | Sends mobile notifications with token and user-key auth |
| `webhook` | Sends JSON events to an arbitrary HTTP endpoint, optionally with bearer auth |

Severity filters are per channel. If no filter is configured, the channel receives all Beacon severities.

## Event Correlation

Beacon events carry a `correlationKey` field in `metadata` that ties related
events into a single incident arc. The key is stable across state transitions
for the same subject — for example, `contextdb:web:health` is the correlation
key for every service health event on that process, whether the event type is
`service.health.critical` or `service.health.recovered`.

Events that represent a state change also carry `previousState` and/or
`previousEventType` in metadata so consumers can reconstruct the transition
without querying prior events.

### Correlation keys by event family

| Event family | Correlation key pattern | Example |
| --- | --- | --- |
| Service health | `{app}:{process}:health` | `contextdb:web:health` |
| Allocation state | `{app}:{taskGroup}:allocation` | `signal-sideband:web:allocation` |
| Task restarts / OOM | `{app}:{taskGroup}:{task}:restarts` | `contextdb:web:contextdb-web:restarts` |
| Cron outcomes | `{app}:{process}:cron` | `field-harbor:backup:cron` |

### Consumer guidance (vigil-gateway, mobile)

Norn emits correlation keys; it does not track open/resolved incident state
itself. Consumers are responsible for:

1. **Grouping**: query events sharing the same `correlationKey` to build an
   incident timeline.
2. **Resolution**: when the most recent event in a correlation group is
   `severity: info` (e.g. `service.health.recovered`), treat the incident as
   resolved. An incident with only `warning`/`critical` events and no recovery
   is still open.
3. **Display**: show the correlation group as a single row with the most recent
   severity as the headline, expandable to the full timeline.
4. **Push suppression**: consider suppressing push notifications for `info`
   recovery events that arrive within a short window of the triggering event
   (e.g. a health blip that resolves in under 60 seconds).

Vigil-gateway currently stores events flat. To support grouped display in the
mobile app, vigil should:

- Index `metadata->>'correlationKey'` in its `beacon_events` table.
- Expose a `GET /api/incidents` endpoint that groups by correlation key,
  returns the most recent severity, and includes a timeline of constituent
  events.
- Forward the `correlationKey` and `previousEventType` fields in APNs payloads
  so the mobile app can update existing notification threads rather than
  creating new ones.

These changes are additive — the existing flat event list continues to work
unchanged.

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
