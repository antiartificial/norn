# Nomad Translator

The translator converts an InfraSpec into Nomad job specifications. There are three translation functions for three job types.

## Translate — Service Jobs

`Translate(spec, imageTag, env)` creates a Nomad **service** job. Each non-scheduled process in the infraspec becomes a TaskGroup within the job.

### Process → TaskGroup Mapping

```
InfraSpec.Processes
├── web (port: 8080)      → TaskGroup "web"     [service job]
├── worker (no port)      → TaskGroup "worker"   [service job]
├── cleanup (schedule)    → SKIPPED (separate periodic job)
└── resize (function)     → SKIPPED (on-demand batch job)
```

Scheduled processes and functions are excluded from the main service job — they get their own jobs via `TranslatePeriodic` and `TranslateBatch`.

### Docker Task Config

Each TaskGroup contains a single Docker task:

```hcl
task "web" {
  driver = "docker"
  config {
    image   = "ghcr.io/user/myapp:abc1234"
    command = "/bin/sh"
    args    = ["-c", "./server"]
    ports   = ["web-http"]
  }
}
```

- `image` — the built and pushed Docker image tag
- `command` / `args` — only set if the process defines a `command` (overrides Docker CMD)
- `ports` — label format: `{processName}-http`

### Port Handling

| Scenario | Port Type | Nomad Config |
|----------|-----------|--------------|
| Process has `port` + app has `endpoints` | Static | `ReservedPorts: [{Label, Value}]` |
| Process has `port`, no `endpoints` | Dynamic | `DynamicPorts: [{Label, To}]` |
| Process has no `port` | None | No network resource |

Static ports are used when endpoints are defined because cloudflared needs a predictable address for routing.

### Consul Service Registration

Processes with a port are registered as Consul services:

```hcl
service {
  name      = "myapp-web"
  port      = "web-http"
  provider  = "consul"

  check {
    type     = "http"
    path     = "/health"
    interval = "10s"
    timeout  = "5s"
  }
}
```

Health checks are added when the process defines a `health` block.

### Environment Variables

Environment variables are merged from two sources:

1. `spec.Env` — static variables from the infraspec
2. `env` parameter — secrets and runtime variables injected by the pipeline

The `env` parameter takes precedence on conflicts.

### Resources

| Field | Default | Override |
|-------|---------|---------|
| CPU | 100 MHz | `process.resources.cpu` |
| Memory | 128 MB | `process.resources.memory` |

### Volume Mounts

Volumes declared in the infraspec are mapped to Nomad host volumes:

```hcl
volume "signal-data" {
  type   = "host"
  source = "signal-data"
}

volume_mount {
  volume      = "signal-data"
  destination = "/var/lib/signal-cli"
  read_only   = false
}
```

### Update Strategy

All service TaskGroups use this update strategy:

| Setting | Value | Purpose |
|---------|-------|---------|
| `MaxParallel` | 1 | Roll one allocation at a time |
| `MinHealthyTime` | 30s | Must be healthy for 30 seconds before continuing |
| `AutoRevert` | true | Automatically revert to last stable version on failure |

### Restart Policy

| Setting | Value |
|---------|-------|
| `Attempts` | 3 |
| `Interval` | 5 minutes |
| `Delay` | 15 seconds |
| `Mode` | delay |

## TranslatePeriodic — Cron Jobs

`TranslatePeriodic(spec, procName, proc, imageTag, env)` creates a Nomad **periodic batch** job for a process with a `schedule`.

- Job ID: `{appName}-{processName}`
- Periodic config uses the cron spec type
- Same environment, resource, and volume handling as service jobs
- No health checks or Consul registration

```yaml
# infraspec
processes:
  cleanup:
    schedule: "0 3 * * *"
    command: ./cleanup
```

Translates to:

```hcl
job "myapp-cleanup" {
  type = "batch"
  periodic {
    cron     = "0 3 * * *"
    enabled  = true
  }
  # ...
}
```

## TranslateBatch — Function Jobs

`TranslateBatch(spec, procName, proc, imageTag, env, jobID)` creates a one-shot Nomad **batch** job for function invocations.

- Job ID: caller-supplied (includes execution ID for uniqueness)
- No retries: restart policy is `attempts: 0, mode: fail`
- `function.memory` overrides `resources.memory` if set
- Same environment and volume handling as other job types
