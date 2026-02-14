# Saga Events

Norn v2 uses an append-only event log (the "saga") instead of mutable step logs on deployment records. Every operation — deploys, restarts, scales, function invocations — emits structured events that form an immutable audit trail.

## Why Append-Only

- **Immutable** — events are never updated or deleted, creating a reliable audit trail
- **No locks** — concurrent operations can safely write events without contention
- **Easy filtering** — query by saga ID, app, category, or time range
- **Debuggable** — full history of what happened, when, and why

## Event Structure

| Field | Type | Description |
|-------|------|-------------|
| `id` | string (UUID) | Unique event identifier |
| `sagaId` | string (UUID) | Groups events belonging to a single operation |
| `timestamp` | time | When the event occurred |
| `source` | string | Who emitted the event (e.g. `pipeline`, `handler`, `cron`) |
| `app` | string | App name |
| `category` | string | Event category |
| `action` | string | Specific action within the category |
| `message` | string | Human-readable description |
| `metadata` | map[string]string | Structured key-value pairs (step name, duration, error, etc.) |

## Categories

| Category | Operations |
|----------|------------|
| `deploy` | Full deploy pipeline |
| `restart` | Rolling restarts |
| `scale` | Task group scaling |
| `system` | Health checks, startup, internal operations |

## Actions

| Action | Meaning |
|--------|---------|
| `step.start` | A pipeline step has begun |
| `step.complete` | A pipeline step finished successfully |
| `step.failed` | A pipeline step failed |
| `deploy.start` | Deploy operation initiated |
| `deploy.complete` | Deploy operation succeeded |
| `deploy.failed` | Deploy operation failed |
| `nomad.submitted` | Job submitted to Nomad |
| `restart.complete` | Rolling restart finished |
| `scale.complete` | Scaling operation finished |

## Storage

Events are stored in the `saga_events` PostgreSQL table. The saga store interface provides four query methods:

```go
type Store interface {
    Append(ctx, evt)         error
    ListBySaga(ctx, sagaID)  ([]Event, error)
    ListByApp(ctx, app, limit) ([]Event, error)
    ListRecent(ctx, limit)   ([]Event, error)
}
```

## Saga Helper

The `Saga` struct provides convenience methods for common event patterns:

```go
sg := saga.New(store, "myapp", "pipeline", "deploy")

sg.Log(ctx, "deploy.start", "deploying myapp", nil)
sg.StepStart(ctx, "clone")
sg.StepComplete(ctx, "clone", 1234)    // duration in ms
sg.StepFailed(ctx, "build", err)
```

Each method appends an event with the saga's ID, app, source, and category pre-filled.

## CLI: `norn saga`

View saga events from the terminal:

```bash
# View events for a specific saga
norn saga abc-123-def

# Recent events for an app
norn saga --app=myapp --limit=50
```

## Formatter

Events are rendered with action icons:

| Action | Icon |
|--------|------|
| `step.start` | `▶` |
| `step.complete` | `✓` |
| `step.failed` | `✗` |
| (other) | `·` |

Output format: `HH:MM:SS <icon> <message>`

```
14:30:01 ▶ clone started
14:30:05 ✓ clone completed
14:30:05 ▶ build started
14:30:42 ✓ build completed
14:30:42 ▶ test started
14:30:48 ✗ test failed: exit code 1
```
