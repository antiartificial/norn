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
GET  /api/events/correlated
GET  /api/events/{id}
POST /api/events
POST /api/events/{id}/ack
POST /api/events/{id}/snooze
POST /api/events/{id}/open
POST /api/events/reconcile
POST /api/incidents/action
GET  /api/events/sinks
POST /api/events/test
GET  /api/alerts/rules
GET  /api/notifications/channels
POST /api/notifications/channels
POST /api/notifications/channels/{id}/test
DELETE /api/notifications/channels/{id}
POST /api/access/tokens
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

## Evidence-Based Reconciliation

Beacon can mechanically review stale open warning and critical events against
newer durable events and the current Nomad or Consul state. Preview every pass
before allowing acknowledgements:

```bash
norn events reconcile --dry-run
norn events reconcile --dry-run --app field-harbor --limit 50
norn events reconcile --by operator
```

The response reports every full event ID, its proposed action, reason, and
supporting evidence. `--dry-run` never changes event state. Without it, only
decisions marked `acknowledge` are acknowledged; unmatched or inconclusive
events remain open as `needs_review`.

Current deterministic rules are deliberately narrow:

| Open event | Evidence required before acknowledgement |
| --- | --- |
| `deploy.failed` | A later `deploy.succeeded` event for the app and a currently running Nomad job with a healthy allocation |
| `service.health.warning` or `service.health.critical` | A later `service.health.recovered` event, or every current Consul check for the affected service is passing |
| `cron.failed`, `cron.lost`, `cron.hung`, or `cron.missed_run` | The Nomad periodic parent is running and unpaused with no running or pending children; a referenced child must be terminal or absent |
| `nomad.task.restarted` | The app is currently healthy and the restart event occurred at least 15 minutes ago, whether the original allocation remains active or has been replaced. The reconciler does not prove continuous health throughout that interval |
| `service.capacity.below_minimum` | A later host-scoped `service.capacity.recovered` event from the same source and environment with the same `norn-host:<host-id>:minimum-capacity` correlation key. The aggregate warning is never partially reconciled for one affected app. |

The host capacity watcher scopes its Beacon source, correlation key, and
dedupe key with a stable host identity. Set `NORN_HOST_ID` (or pass
`norn host ... --host-id`) when a physical host needs an operator-owned ID;
otherwise the runtime persists its first short hostname in its assurance state.
The watcher keeps that correlation key stable for one unresolved episode. Its
warning dedupe key combines an episode token and the current missing-capacity
snapshot hash, so a changed set emits a new warning; its recovery dedupe key
remains episode-only. It commits or removes its local capacity state only after
Beacon accepts the corresponding event. An info
recovery automatically acknowledges only warning/critical events from the same
source, app, environment, correlation key, and no later than the recovery
event; other hosts, environments, apps, correlations, and later observations
remain open.

The legacy `norn-host:minimum-capacity` key remains recognizable for existing
Mini events. On the first identity-scoped recovery, the runtime may adopt one
unacknowledged legacy warning only when its retained local snapshot matches
exactly and `NORN_BEACON_ENVIRONMENT` (or `--beacon-environment`) names the
same Beacon environment. The chosen environment is persisted for periodic
assurance. Missing environment configuration is retryable and leaves the local
snapshot intact; zero or multiple in-environment candidates stay open for
review.

The reconciliation endpoint accepts `app`, `limit`, `dryRun`, and `by` in its
JSON body. It does not infer recovery from an old acknowledgement, a matching
message string, or the mere passage of time. Missing substrate connections,
ambiguous child-job state, and health without the required evidence remain for
operator review.

Incident groups can also be acted on directly. Use `correlationKey` for modern
Beacon event families and `dedupeKey` for older or external events that do not
carry a correlation key. Key-only actions remain compatible with older
clients; include `source`, `app`, and `environment` when acting on a group
returned by the scoped active-incidents view.

```http
POST /api/incidents/action
```

```json
{
  "action": "resolve",
  "correlationKey": "field-harbor:field-harbor-sync-pm:cron",
  "source": "norn-host:mini-1",
  "app": "field-harbor",
  "environment": "mini",
  "by": "operator",
  "note": "periodic parent is healthy"
}
```

`resolve` acknowledges existing warning/critical events and emits an
`incident.resolved` info event through Beacon, so sinks such as Vigil see a real
recovery event rather than a silent local state change.

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

### Bootstrap

`norn notifications bootstrap` auto-discovers services from the service manifest and creates default notification channels. Currently it discovers vigil-gateway and creates a webhook channel pointing to its `/api/events` endpoint, filtered to `warning` and `critical` severities. If a vigil webhook channel already exists, it is skipped.

```bash
norn notifications bootstrap
```

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
| Deploy lifecycle | `{app}:deploy` | `field-harbor:deploy` |
| Rollback lifecycle | `{app}:rollback` | `field-harbor:rollback` |
| Canary promotion | `{app}:deploy` | `contextdb:deploy` |
| Snapshot operations | `{app}:snapshots` | `field-harbor:snapshots` |
| Cron outcomes | `{app}:{process}:cron` | `field-harbor:backup:cron` |

### Querying correlated events

`GET /api/events/correlated?key=<correlationKey>` retains key-only,
backward-compatible lookup. For a split incident group, add `source`, `app`,
and `environment` to retrieve only that physical timeline; results are ordered
chronologically (oldest first):

```bash
norn events correlated contextdb:web:health
norn events correlated field-harbor:deploy --limit 10
```

### Auto-acknowledgement on resolution

For ordinary event families, an `info`-severity event with a `correlationKey`
(e.g. `service.health.recovered`, `deploy.succeeded`) automatically
acknowledges matching open `warning` and `critical` events in the same source,
app, environment, and correlation scope. Capacity is deliberately narrower:
only `service.capacity.recovered` can close capacity warnings; current
host-scoped keys close capacity warnings in that scope, while a legacy global
key must name one exact `legacyCapacityWarningID`. The acknowledgement note
records which event resolved the warning, e.g. `resolved by evt_abc123`.

This keeps `norn events` focused on what still needs attention rather than
showing resolved noise alongside active incidents.

### Resolution semantics

An incident is **resolved** when its scoped timeline has no unacknowledged,
unsnoozed warning or critical events. A later informational recovery does not
resolve an older unmatched warning; active-incidents instead represents that
timeline with its newest remaining open warning or critical event.

### Vigil-gateway integration

Vigil-gateway indexes `metadata->>'correlationKey'` and exposes
`GET /api/incidents` to group events by correlation key. Each incident includes
the most recent severity, event count, first/last seen timestamps, resolved
status, and the full event timeline.

APNs push notifications use `correlationKey` as the `thread-id` so iOS groups
related notifications into a single thread. Recovery events update the existing
thread rather than creating a new notification.

### CLI incident links

`norn events show <id>` displays the correlation key and the command to view
the full incident timeline when the event has a `correlationKey` in metadata:

```bash
norn events show evt_abc123
# ...
# incident  norn events correlated contextdb:web:health
```

## Event Deduplication

Beacon suppresses duplicate events at emit time. When an event has a
`dedupeKey` that matches an event emitted within the last hour, the new
event is silently dropped. This prevents event storms from repeated watcher
detections of the same condition — for example, a hung cron job or a
persistently failed allocation will emit one event per condition rather
than one per check cycle.

The watcher's in-memory `seen` map provides first-pass dedup within a
single API process lifetime. The database-backed dedup window survives
API restarts, so a platform upgrade does not re-emit all previously
observed conditions.

## Active Incidents

`GET /api/events/active` returns unresolved incident groups, scoped by
source, app, environment, and correlation key. A group remains active when it
has an open (unacknowledged, unsnoozed) warning or critical event; its displayed
representative is the newest such open event, not a later informational
recovery or adoption row.

```bash
norn events active
norn events active --limit 10
```

Each incident shows the correlation key, source, app, environment, latest open
severity, full-timeline event count, and first/last seen timestamps. Use
`norn events correlated <key>` for key-only compatibility or the scoped API to drill
into a specific incident's full timeline.

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
