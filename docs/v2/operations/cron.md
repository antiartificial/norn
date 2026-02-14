# Cron Jobs

Cron processes run on a schedule as Nomad periodic batch jobs.

## Defining a Cron Process

Add a process with a `schedule` field to your infraspec:

```yaml
processes:
  cleanup:
    schedule: "0 3 * * *"
    command: ./cleanup
    resources:
      cpu: 100
      memory: 256
```

The `schedule` field uses standard cron syntax. This process is excluded from the main service job and gets its own Nomad periodic batch job with ID `{appName}-{processName}`.

## Nomad Periodic Jobs

The translator creates a separate periodic batch job:

```hcl
job "myapp-cleanup" {
  type = "batch"
  periodic {
    cron    = "0 3 * * *"
    enabled = true
  }
  group "cleanup" {
    task "cleanup" {
      driver = "docker"
      config {
        image   = "ghcr.io/user/myapp:abc1234"
        command = "/bin/sh"
        args    = ["-c", "./cleanup"]
      }
    }
  }
}
```

## CLI Management

```bash
# View cron status
norn cron myapp

# Trigger a cron job immediately (outside schedule)
norn cron myapp trigger

# Pause the periodic job
norn cron myapp pause

# Resume a paused job
norn cron myapp resume

# Update the schedule
norn cron myapp schedule "0 6 * * *"
```

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/apps/{id}/cron/history` | Execution history |
| POST | `/api/apps/{id}/cron/trigger` | Trigger immediately |
| POST | `/api/apps/{id}/cron/pause` | Pause the periodic job |
| POST | `/api/apps/{id}/cron/resume` | Resume a paused job |
| PUT | `/api/apps/{id}/cron/schedule` | Update the cron expression |

## Execution History

View past executions in the UI's cron panel or via the API:

```bash
curl http://localhost:8800/api/apps/myapp/cron/history
```

Each execution shows:
- Start time and duration
- Exit code
- Nomad allocation ID

## Multiple Cron Processes

An app can have multiple cron processes — each becomes a separate Nomad periodic job:

```yaml
processes:
  daily-cleanup:
    schedule: "0 3 * * *"
    command: ./cleanup
  hourly-digest:
    schedule: "0 * * * *"
    command: ./digest
```

This creates two Nomad jobs: `myapp-daily-cleanup` and `myapp-hourly-digest`.
