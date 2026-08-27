# Cloudflare

Norn v2 uses Cloudflare Tunnels (cloudflared) for external routing and optionally Cloudflare Access for API authentication.

## Tunnel Routing

Homebrew supplies the cloudflared binary, while Norn owns the persistent
`com.norn.cloudflared` LaunchAgent and its config file. During the **forge**
step, Norn validates a candidate config, atomically publishes it, and restarts
that managed tunnel process.

### How It Works

Norn manages cloudflared's config file directly (default `~/.cloudflared/config.yml`). No Kubernetes or Docker dependency is required — cloudflared runs as a native macOS service.

| Component | Details |
|-----------|---------|
| Config file | `~/.cloudflared/config.yml` (override with `NORN_CLOUDFLARED_CONFIG`) |
| Process management | Norn LaunchAgent (`com.norn.cloudflared`) |
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

3. Install and activate the Norn-owned service:

```bash
norn host install --repo /path/to/norn \
  --cloudflared-config ~/.cloudflared/config.yml \
  --cloudflared-probe https://myapp.example.com/health
norn host cloudflared-recover \
  --cloudflared-probe https://myapp.example.com/health
```

::: warning Do not use `brew services` for cloudflared
`brew services start/restart cloudflared` regenerates
`homebrew.mxcl.cloudflared` with only the binary path. That daemon exits or
crash-loops because it has no named-tunnel arguments. Homebrew upgrades may
replace the binary; the Norn LaunchAgent follows the stable Homebrew symlink.
After `brew upgrade cloudflared`, use `norn host cloudflared-recover`.
:::

After a macOS upgrade, Homebrew update, or reboot, run `norn host doctor` and
verify `com.norn.cloudflared`. The login supervisor also validates and recovers
the tunnel before restarting the Norn API.

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
norn endpoints myapp

# Toggle a single hostname
norn endpoints toggle myapp app.example.com
```

**Via API:**

```bash
# List active ingress hostnames
curl http://localhost:8800/api/cloudflared/ingress

# Enable an endpoint
curl -X POST http://localhost:8800/api/apps/myapp/endpoints/toggle \
  -H "Content-Type: application/json" \
  -d '{"hostname": "app.example.com", "enabled": true}'

# Disable an endpoint
curl -X POST http://localhost:8800/api/apps/myapp/endpoints/toggle \
  -H "Content-Type: application/json" \
  -d '{"hostname": "app.example.com", "enabled": false}'
```

### Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `NORN_CLOUDFLARED_CONFIG` | `~/.cloudflared/config.yml` | Path to the cloudflared config file |
| `NORN_CLOUDFLARED_BIN` | auto-detected | cloudflared binary used for validation |
| `NORN_CLOUDFLARED_LAUNCH_LABEL` | `com.norn.cloudflared` | LaunchAgent restarted after config changes |

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

When both variables are set, the API middleware validates the
`Cf-Access-Jwt-Assertion` header. A validated identity becomes an explicit
admin principal for the operator UI and satisfies
`NORN_REQUIRE_EXPLICIT_AUTH=true`; an unvalidated identity header never grants
access.

Public control-plane routes are intentionally narrow:
- `/api/health` — health check
- `/api/version` — version endpoint
- `/api/webhooks/*` — webhook receivers
- `/api/access/cloudflare/logpush` — Cloudflare Logpush receiver with its own shared-secret header

`/api/v1/events` (and its `/ws` compatibility alias), compatibility exec, and
`/api/v1/exec-sessions/*` are not bearer-auth exemptions. Scoped access tokens
require `events:read` and `apps:exec` respectively; formal exec creation also
consumes an app-bound device-key step-up capability. Cloudflare Access can
protect the same paths at the edge.
Forwarded client-IP headers are honored only when the direct socket peer is
loopback, preventing a direct tailnet or LAN client from spoofing the local
proxy trust boundary. The wake gateway also strips Cloudflare identity headers
from requests that did not arrive through that boundary before proxying them to
an application.

### Combining with Bearer Token

Both CF Access and bearer token auth can be enabled simultaneously. A request
may authenticate with a validated Cloudflare Access identity or a scoped bearer
token. This keeps the browser UI credential-free while native clients and the
CLI use device or control-plane tokens.

```bash
# Both enabled
export NORN_CF_ACCESS_TEAM_DOMAIN=myteam.cloudflareaccess.com
export NORN_CF_ACCESS_AUD=abc123...
export NORN_API_TOKEN="$(openssl rand -base64 32)"
```

## Access Observations

Norn can use Cloudflare traffic data as an access-pattern signal for the advisory resource tuner. This is useful when public services are reached through cloudflared and the Norn control plane would otherwise only see API traffic, not app traffic.

There are two supported ingestion paths:

| Path | Use | Required configuration |
| --- | --- | --- |
| GraphQL sync | Backfill or periodically import hourly request counts by hostname | `NORN_CLOUDFLARE_API_TOKEN`, `NORN_CLOUDFLARE_ZONE_ID` |
| HTTP Logpush | Continuously receive request logs from Cloudflare | `NORN_CLOUDFLARE_LOGPUSH_TOKEN` |

`norn access cloudflare status` reports whether the GraphQL and Logpush credentials are configured and lists the public service hostnames Norn can map to app/process pairs.

`norn access cloudflare sync --window 14d` queries Cloudflare's GraphQL Analytics API for each mapped public hostname and records hourly aggregate observations as `cloudflare-graphql`. The Cloudflare token must be able to read analytics for the configured zone. Norn records only aggregate counts and status buckets; it does not persist request bodies or authorization headers.

GraphQL syncs are split into day-sized Cloudflare queries and clamped to Norn's configured GraphQL lookback ceiling. A requested 14-day import can therefore produce a shorter effective window when the Cloudflare zone only exposes recent analytics. Re-running the sync is idempotent for the same hourly aggregate buckets: GraphQL-imported buckets replace the previous `cloudflare-graphql` value instead of adding to it, so retries and interrupted runs do not inflate traffic counts.

Cloudflare Logpush can deliver HTTP request logs to:

```text
https://<norn-host>/api/access/cloudflare/logpush
```

Set a secret header in the Logpush destination URL, for example:

```text
?header_X-Norn-Logpush-Token=<random-token>
```

Store the same value as `NORN_CLOUDFLARE_LOGPUSH_TOKEN` in the Norn API secret bundle. The receiver also accepts `X-Logpush-Secret` and `Authorization: Bearer <token>` for compatibility with existing Logpush setups. Keep this endpoint HTTPS-only and protected by the shared secret.

Imported observations feed `/api/access/patterns` and `/api/tuning/recommendations`, allowing idle candidates and active windows to be based on Cloudflare traffic instead of manual observations.

## Wake Gateway

Norn can sit on the live request path for selected public endpoints through the wake gateway. Use this when a public service may be scaled down and should wake on the next request.

### Production Setup

Point each wakeable public hostname at the Norn API origin. Do not add a path prefix.

```yaml
ingress:
  - hostname: app.example.com
    service: http://127.0.0.1:8800
  - service: http_status:404
```

The only required routing header is the original public `Host` header. cloudflared preserves this automatically for hostname ingress rules. Generic reverse proxies must do the same:

```nginx
proxy_set_header Host $host;
proxy_set_header X-Forwarded-Host $host;
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_pass http://127.0.0.1:8800;
```

With host-based routing, a request stays in its normal shape:

```text
https://app.example.com/archive/123
```

Norn receives `Host: app.example.com`, maps that hostname to a public endpoint from the service manifest, wakes the mapped app/process if needed, and proxies `/archive/123` unchanged to the service.

### Local Smoke Test

From the Norn host:

```bash
curl -i -H "Host: app.example.com" http://127.0.0.1:8800/health
```

A gateway-handled response includes:

```text
X-Norn-Wake-Gateway: true
X-Norn-Wake-Action: ready
```

`X-Norn-Wake-Action: scaled` means Norn had to scale the mapped process from zero before proxying the request.

### Explicit Path Form

The explicit API route is useful for local tests or proxies that cannot preserve the original `Host` header:

```text
https://<norn-host>/api/wake-gateway/<public-hostname>/<original-path>
```

Example:

```bash
curl -i http://127.0.0.1:8800/api/wake-gateway/app.example.com/health
```

This strips `/api/wake-gateway/app.example.com` before proxying, so the service receives `/health`.

### Behavior

The gateway maps the public hostname back to a service endpoint from the service manifest, records a `wake-gateway` access observation, checks for a passing Consul instance, and reverse-proxies to that instance. If no passing instance exists, it scales the mapped Nomad task group to `1`, waits for readiness, then proxies the request. Requests that cannot wake before the bounded timeout return `504` with `Retry-After`.

The default wake wait is `30s`. A request can override it with `wakeTimeout`, up to `2m`:

```text
https://app.example.com/archive/123?wakeTimeout=60s
```

The gateway removes `wakeTimeout` before forwarding the request to the service.

This route is intentionally hostname-mapped and does not proxy arbitrary upstream URLs. Point only selected cloudflared or local proxy rules at it, and keep direct Norn API access controlled separately.
