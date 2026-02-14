# Nomad & Consul

Norn v2 uses HashiCorp Nomad for job scheduling and Consul for service discovery. This replaces the Kubernetes/minikube stack from v1.

## Nomad

Nomad schedules and runs all Norn workloads as Docker containers.

### Job Types

| Type | Used For | Norn Process Type |
|------|----------|-------------------|
| **Service** | Long-running processes | `web`, `worker` |
| **Batch** | One-shot executions | `function` invocations |
| **Periodic Batch** | Scheduled jobs | `cron` processes |

### Allocation Lifecycle

1. Job submitted → Nomad schedules allocations on available nodes
2. Docker task starts within the allocation
3. Health checks run (if configured)
4. Allocation marked healthy after `MinHealthyTime` (30s)
5. On update: rolling deploy with `MaxParallel=1`, `AutoRevert=true`

### Node Requirements

Nomad client nodes need `host_volume` stanzas for any apps that use volumes:

```hcl
client {
  host_volume "signal-data" {
    path      = "/opt/volumes/signal-data"
    read_only = false
  }
}
```

### Dev Mode

```bash
# Start Nomad in dev mode (single node, in-memory)
make nomad-dev
# or
nomad agent -dev -bind=0.0.0.0
```

Dev mode runs at `http://localhost:4646`. The Nomad UI is available at the same address.

### Production

For production, configure Nomad server and client separately:

```hcl
# /etc/nomad.d/server.hcl
server {
  enabled          = true
  bootstrap_expect = 1
}

datacenter = "dc1"
data_dir   = "/opt/nomad"
```

```hcl
# /etc/nomad.d/client.hcl
client {
  enabled = true
}

datacenter = "dc1"
data_dir   = "/opt/nomad"
```

## Consul

Consul provides service discovery and health checking. Nomad registers services with Consul automatically when the translator adds `service` blocks.

### Service Registration

Each process with a port gets a Consul service:

- Service name: `{appName}-{processName}` (e.g. `myapp-web`)
- Health check: HTTP check on the configured path, interval, and timeout
- Provider: `consul` (Nomad native integration)

### DNS Discovery

Services are discoverable via Consul DNS:

```bash
dig @127.0.0.1 -p 8600 myapp-web.service.consul
```

### Dev Mode

```bash
# Start Consul in dev mode
make consul-dev
# or
consul agent -dev
```

Dev mode runs at `http://localhost:8500`. The Consul UI is available at the same address.

### Production

```hcl
# /etc/consul.d/server.hcl
server           = true
bootstrap_expect = 1
datacenter       = "dc1"
data_dir         = "/opt/consul"
ui_config {
  enabled = true
}
```

## Norn Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_NOMAD_ADDR` | `http://localhost:4646` | Nomad API address |
| `NORN_CONSUL_ADDR` | `http://localhost:8500` | Consul API address |

The API connects to both on startup and logs warnings if either is unavailable. Operations that depend on Nomad/Consul will fail gracefully if the services are down.
