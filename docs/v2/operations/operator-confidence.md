# Operator Confidence

The operator-confidence release collects Norn's incident, cron, deploy,
snapshot, wake, authentication, and mobile-action surfaces into one API and CLI
contract.

Use it when you want to answer one question quickly: what should an operator do
next?

## CLI

```bash
norn operator inbox
norn operator cron
norn operator wake-targets
norn operator deploy-confidence
norn operator snapshot-readiness
norn operator auth-hints
norn operator actions
```

`norn operator inbox` is the high-signal entry point. It combines active Beacon
incidents, active or failed operations, deploy risks, cron risks, snapshot
readiness, secret status, and wake target counts.

## API

```http
GET  /api/operator/inbox
GET  /api/operator/cron
GET  /api/operator/wake-targets
GET  /api/operator/deploy-confidence
GET  /api/operator/snapshot-readiness
GET  /api/operator/auth-hints
GET  /api/operator/actions
POST /api/incidents/action
```

The operator endpoints are read-only except `POST /api/incidents/action`.
Action endpoints return API paths that already exist elsewhere in Norn, so
mobile clients and operator tools can render actions without hardcoding every
workflow.

## Incident Lifecycle

Beacon events remain the immutable event history. Incident lifecycle state is
derived from event groups and operator actions.

Correlated incidents use `metadata.correlationKey`. Older or external events
can still fall back to `dedupeKey`.

```json
{
  "action": "resolve",
  "correlationKey": "field-harbor:field-harbor-sync-pm:cron",
  "app": "field-harbor",
  "by": "operator",
  "note": "periodic parent is healthy"
}
```

Supported actions:

| Action | Effect |
| --- | --- |
| `acknowledge` | Marks warning/critical events in the group as acknowledged |
| `snooze` | Temporarily suppresses the group until `until` or `duration` elapses |
| `open` | Clears acknowledgement and snooze state for the group |
| `resolve` | Acknowledges the group and emits an `incident.resolved` info event |

Resolution goes through Beacon so downstream sinks such as Vigil receive the
same recovery arc Norn sees locally.

## Cron And Function Operations

`GET /api/operator/cron` summarizes every scheduled process across discovered
apps. Each entry includes schedule, timezone, local next-run and last-run text,
Nomad periodic parent status, child counters, and action URLs for trigger,
pause, or resume.

Cron remains controlled by the existing app endpoints:

```http
POST /api/apps/{id}/cron/trigger
POST /api/apps/{id}/cron/pause
POST /api/apps/{id}/cron/resume
PUT  /api/apps/{id}/cron/schedule
```

Function invocation history remains under:

```http
GET  /api/apps/{id}/function/history
POST /api/apps/{id}/invoke
```

## Wake-On-Request Targets

`GET /api/operator/wake-targets` lists public, private, and local endpoints
from the service manifest with their readiness and wake-gateway URL.

The wake gateway remains Host-based for production routing. If a proxy cannot
preserve Host, the fallback path is:

```http
/api/wake-gateway/{public-hostname}/{original-path}
```

## Deploy Confidence

`GET /api/operator/deploy-confidence` summarizes each app's recent deployment
record, last status, source-dirty evidence, canary processes, and
`deployPolicy.autoRollback`.

Use it before a real deploy:

```bash
norn operator deploy-confidence
norn preflight <app> HEAD
norn deploy <app> HEAD
```

## Snapshot Readiness

`GET /api/operator/snapshot-readiness` reports app-owned Postgres snapshots,
retention overages, latest local restore point, remote export configuration, and
pre-restore policy.

Norn only treats databases declared in an app's `infraspec.yaml` as app-owned.
External/shared databases should not be declared as app-owned snapshot
infrastructure.

## Secret-Safe Auth Hints

`GET /api/operator/auth-hints` documents secret-safe operational patterns. It is
deliberately descriptive: it gives commands and rules of thumb, not secret
values.

For Mini-hosted Vigil gateway work, the important pattern is to use Nomad
`alloc exec -t=false` so pseudo-TTY handling does not corrupt environment
captures. Report only presence, length, or short fingerprints when validating
secrets.

## Mobile Actions

`GET /api/operator/actions` returns action descriptors with method, path,
schema, risk, and whether the action is mobile-ready. Mobile clients should use
this catalogue to render guarded actions such as:

| Action | Risk |
| --- | --- |
| Acknowledge, snooze, or resolve an incident | Low |
| Trigger or pause cron | Medium |
| Run preflight | Low |
| Deploy or restore snapshot | High |

High-risk actions should require explicit confirmation in mobile clients.
