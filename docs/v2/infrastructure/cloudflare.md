# Cloudflare

Norn v2 uses Cloudflare Tunnels (cloudflared) for external routing and optionally Cloudflare Access for API authentication.

## Tunnel Routing

cloudflared runs locally as a Homebrew LaunchAgent, managed via a config file on disk. During the **forge** step of the deploy pipeline, Norn reads the config, updates ingress rules, writes it back, and restarts the tunnel process.

### How It Works

Norn manages cloudflared's config file directly (default `~/.cloudflared/config.yml`). No Kubernetes or Docker dependency is required — cloudflared runs as a native macOS service.

| Component | Details |
|-----------|---------|
| Config file | `~/.cloudflared/config.yml` (override with `NORN_CLOUDFLARED_CONFIG`) |
| Process management | Homebrew LaunchAgent (`homebrew.mxcl.cloudflared`) |
| Restart method | `launchctl kickstart -k` (kills + relaunches immediately) |
| Tunnel type | Named tunnel with credentials file |

### Setup

1. Install cloudflared and create a named tunnel:

```bash
brew install cloudflared
cloudflared tunnel login
cloudflared tunnel create multi-domain-tunnel
```

2. Configure `~/.cloudflared/config.yml`:

```yaml
tunnel: multi-domain-tunnel
credentials-file: /Users/you/.cloudflared/multi-domain-tunnel.json

ingress:
  - hostname: myapp.example.com
    service: http://192.168.4.124:3001
  - service: http_status:404    # catch-all (required)
```

3. Update the Homebrew plist to include `tunnel run` arguments:

```xml
<key>ProgramArguments</key>
<array>
  <string>/opt/homebrew/opt/cloudflared/bin/cloudflared</string>
  <string>tunnel</string>
  <string>run</string>
</array>
```

::: warning Homebrew default plist
The default Homebrew plist for cloudflared only includes the binary path with no arguments. Without `tunnel run`, cloudflared exits immediately and the LaunchAgent crash-loops. Always verify the plist includes the `tunnel` and `run` arguments.
:::

4. Start the service:

```bash
# If a system-level daemon exists (token-based), unload it first:
sudo launchctl unload /Library/LaunchDaemons/com.cloudflare.cloudflared.plist

# Start the Homebrew service:
brew services start cloudflared
```

### Infraspec Configuration

```yaml
endpoints:
  - url: https://myapp.example.com
  - url: https://myapp-staging.example.com
    region: us-east
```

### What Forge Does

1. Reads the app's endpoints from the infraspec
2. Finds the Nomad allocation's node address and static port
3. Updates cloudflared's ingress rules to route each hostname to the service
4. Writes the config file and restarts cloudflared via `launchctl kickstart -k`

### What Teardown Does

`norn teardown <app>` removes the app's entries from the cloudflared ingress configuration.

### Per-Endpoint Toggle

You can enable or disable individual endpoints without affecting the rest of the app's routing. This is useful for temporarily taking a hostname offline (e.g. during maintenance) without tearing down all endpoints.

**From the dashboard:** each external endpoint badge shows a cloud toggle icon. A green cloud means the endpoint is active in cloudflared; a dim cloud-slash means it's inactive. Click the icon to toggle.

**From the CLI:**

```bash
# List endpoints with their cloudflared status
norn endpoints signal-sideband

# Toggle a single hostname
norn endpoints toggle signal-sideband sideband.slopistry.com
```

**Via API:**

```bash
# List active ingress hostnames
curl http://localhost:8800/api/cloudflared/ingress

# Enable an endpoint
curl -X POST http://localhost:8800/api/apps/signal-sideband/endpoints/toggle \
  -H "Content-Type: application/json" \
  -d '{"hostname": "sideband.slopistry.com", "enabled": true}'

# Disable an endpoint
curl -X POST http://localhost:8800/api/apps/signal-sideband/endpoints/toggle \
  -H "Content-Type: application/json" \
  -d '{"hostname": "sideband.slopistry.com", "enabled": false}'
```

### Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `NORN_CLOUDFLARED_CONFIG` | `~/.cloudflared/config.yml` | Path to the cloudflared config file |

### Config File Format

Norn reads and writes the standard cloudflared config format:

```yaml
tunnel: multi-domain-tunnel
credentials-file: /Users/you/.cloudflared/multi-domain-tunnel.json

ingress:
  - hostname: app1.example.com
    service: http://192.168.4.124:3001
  - hostname: app2.example.com
    service: http://192.168.4.124:8080
  - service: http_status:404
```

The catch-all rule (`service: http_status:404`) must be the last entry. Norn always inserts new rules before it.

### Host Networking Considerations

Nomad runs Docker containers in **bridge networking** by default. This affects how cloudflared routes reach your services:

| Network Mode | Service Address | Use When |
|-------------|----------------|----------|
| Bridge (default) | `http://<node-ip>:<static-port>` | Standard setup. Nomad reserves a static port on the host when endpoints are defined. |
| Host (`network_mode: host`) | `http://127.0.0.1:<port>` | When your app needs to reach host-local services (e.g. signal-cli on localhost). |

**Bridge mode example** (default — forge handles this automatically):

```yaml
# infraspec.yaml
processes:
  web:
    port: 3001

endpoints:
  - url: https://myapp.example.com
```

Forge resolves the Nomad allocation's node address (e.g. `192.168.4.124`) and writes:

```yaml
# ~/.cloudflared/config.yml (managed by Norn)
ingress:
  - hostname: myapp.example.com
    service: http://192.168.4.124:3001
```

**When your app connects to host-local services** (e.g. a database or signal-cli on localhost), the Docker container can reach the host via `host.docker.internal` — Docker Desktop resolves this to the macOS host automatically. Use this in env vars:

```yaml
# infraspec.yaml
env:
  DATABASE_URL: postgres://norn:norn@host.docker.internal:5432/mydb?sslmode=disable
  SIGNAL_URL: http://host.docker.internal:8080/v1/receive/+1234567890
```

::: warning 127.0.0.1 vs host.docker.internal
Inside a Docker container with bridge networking, `127.0.0.1` refers to the **container's own loopback**, not the host. Use `host.docker.internal` to reach services on the macOS host. This applies to all Nomad Docker tasks unless `network_mode: host` is explicitly set.
:::

### Port Handling

When endpoints are defined, the Nomad translator uses **static ports** instead of dynamic ports. This ensures the service is always reachable at a predictable address for cloudflared routing.

## Cloudflare Access

Norn can validate Cloudflare Access JWTs to authenticate API requests.

### Setup

1. Create a Cloudflare Access application for your Norn instance
2. Set the environment variables:

| Variable | Description |
|----------|-------------|
| `NORN_CF_ACCESS_TEAM_DOMAIN` | Your Cloudflare Access team domain (e.g. `myteam.cloudflareaccess.com`) |
| `NORN_CF_ACCESS_AUD` | The Application Audience (AUD) tag from your Access policy |

### How It Works

When both variables are set, the API middleware validates the `Cf-Access-Jwt-Assertion` header on every request (except exempt routes).

Exempt routes (no auth required):
- `/ws` — WebSocket
- `/api/health` — health check
- `/api/version` — version endpoint
- `/api/webhooks/*` — webhook receivers
- `/api/apps/*/exec` — exec into allocations

### Combining with Bearer Token

Both CF Access and bearer token auth can be enabled simultaneously. The request must pass whichever auth checks are configured.

```bash
# Both enabled
export NORN_CF_ACCESS_TEAM_DOMAIN=myteam.cloudflareaccess.com
export NORN_CF_ACCESS_AUD=abc123...
export NORN_API_TOKEN=secret-token
```
