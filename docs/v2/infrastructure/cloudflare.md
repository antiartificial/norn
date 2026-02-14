# Cloudflare

Norn v2 uses Cloudflare Tunnels (cloudflared) for external routing and optionally Cloudflare Access for API authentication.

## Tunnel Routing

The `endpoints` field in an infraspec maps external URLs to your app's Consul service. During the **forge** step of the deploy pipeline, Norn updates the cloudflared tunnel's ingress configuration.

### Infraspec Configuration

```yaml
endpoints:
  - url: myapp.example.com
  - url: myapp-staging.example.com
    region: us-east
```

### What Forge Does

1. Reads the app's endpoints from the infraspec
2. Resolves the Consul service address for the app's service process
3. Updates cloudflared's ingress rules to route each URL to the service
4. Reloads the cloudflared configuration

### What Teardown Does

`norn teardown <app>` removes the app's entries from the cloudflared ingress configuration.

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
