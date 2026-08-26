# Workload Connectors

Norn separates deployment orchestration from the system that actually runs a
workload. The connector is selected once for the Norn API process:

```text
deploy operation -> workload connector -> scheduler/runtime + discovery
```

The default remains `nomad-consul`. It is the production connector and retains
regional placement, multiple endpoint allocations, Traefik/Consul discovery,
canaries, cron, batch execution, and Nomad's reconciliation behavior.

The optional `apple-container` connector runs development workloads directly
with Apple's native `container` CLI on macOS. Norn owns local reconciliation,
restart supervision, explicit HTTP readiness probes, logs, legacy scoped exec,
resource sampling, and loopback-only published ports. Connector selection is
never automatic.

## Select a connector

```bash
# Default and the only production-supported value
export NORN_WORKLOAD_CONNECTOR=nomad-consul

# Explicit local macOS development mode
export NORN_WORKLOAD_CONNECTOR=apple-container
container system start
```

Inspect the active choice and every known connector:

```bash
norn runtime
curl -H "Authorization: Bearer $NORN_TOKEN" \
  http://127.0.0.1:8800/api/v1/host/runtime
```

An unavailable selected connector fails deployment validation; Norn does not
silently fall back to Docker, Nomad, or another local runtime.

## Capability matrix

| Capability | `nomad-consul` | `apple-container` |
|---|---:|---:|
| Production admission | yes | no |
| Linux and macOS control hosts | yes | macOS only |
| Multiple regions | yes | no; implicit `local` only |
| Multiple worker allocations | yes | yes |
| Multiple endpoint allocations | yes, through regional ingress | not yet |
| Consul/Traefik discovery | yes | no; loopback publication |
| Canary deployments | yes | not yet |
| Cron and batch/functions | yes | not yet |
| Norn HTTP readiness gate | via Nomad/Consul | yes |
| Logs and scoped compatibility exec | yes | yes |
| Formal audited exec sessions | yes | not yet |
| Restart reconciliation | Nomad | Norn local supervisor |

The Apple runtime does not currently provide a complete OCI healthcheck
observer. Norn therefore executes the InfraSpec HTTP readiness probe itself;
mere process liveness never promotes a deployment with a configured health
path.

## Local endpoint behavior

Native ports bind only to loopback. `hostPort` is honored when present;
otherwise the process `port` is used:

```yaml
processes:
  web:
    port: 8080
    hostPort: 18080
    health:
      path: /health
```

Public Cloudflare routes can target that loopback port in local mode. Starting
a second endpoint allocation or any canary is rejected until a native
local ingress owns atomic port switching and balancing. Worker processes
without ports may scale to multiple local containers.

Endpoint updates are currently stop/start on the published port and can have a
brief interruption. Use `nomad-consul` when rolling, canary, or blue/green
traffic behavior is required.

## Security and recovery

Norn writes resolved environment values to a mode `0600` temporary env file
for `container run`; secret values are not placed directly in command-line
arguments. The file is removed after the runtime accepts the container
configuration. Restart operations use `container stop` followed by
`container start`, preserving the original environment and mounts.

On API restart, the native engine lists all Norn-owned containers, rebuilds its
inventory from deterministic names and the current InfraSpecs, and resumes
health and restart supervision. When native mode is explicitly selected, Norn
also starts the Apple container system and waits up to 60 seconds for it to
become ready. The connector does not claim multi-host HA,
durable scheduling, or production readiness. Production startup explicitly
requires `NORN_WORKLOAD_CONNECTOR=nomad-consul`.

## Current verification boundary

The connector, parsers, command construction, secret handling, validation, API
schema, and Nomad compatibility path have unit coverage. The Apple command
contract was also smoke-tested on macOS 26.6 with signed `container` 1.3.0:
system initialization, kernel installation, OCI image pull, run, JSON list and
inspect, logs, non-streaming JSON stats, stop, and removal all completed
successfully. CI remains runtime-independent, and Norn reports the connector
unavailable when the CLI or its system service is absent.
