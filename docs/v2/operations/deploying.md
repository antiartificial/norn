# Deploying

## CLI Deploy

```bash
norn deploy <app> [ref]
```

- `ref` defaults to `HEAD` (latest commit on the configured branch)
- The CLI connects to the WebSocket and shows real-time progress for each pipeline step
- On completion, prints the saga ID for later inspection

Run a preflight first when you want the build/test confidence without runtime mutation:

```bash
norn preflight <app> [ref]
# alias
norn check <app> [ref]
```

Preflight runs `validate`, `clone`, `inspect`, `build`, and `test`. It intentionally skips `snapshot`, `migrate`, `submit`, `healthy`, and `forge`.

Example:

```bash
$ norn deploy signal-sideband abc1234
▶ clone
✓ clone (3.2s)
▶ build
✓ build (37.1s)
▶ test
✓ test (5.8s)
▶ snapshot
✓ snapshot (1.2s)
▶ migrate
✓ migrate (0.4s)
▶ submit
✓ submit (0.8s)
▶ healthy
✓ healthy (32.0s)
▶ forge
✓ forge (1.1s)
▶ cleanup
✓ cleanup (0.2s)

deployed signal-sideband → ghcr.io/antiartificial/signal-sideband:abc1234
saga: f47ac10b-58cc-4372-a567-0e02b2c3d479
```

## UI Deploy

Click the **Deploy** button on an app card in the dashboard. The deploy panel shows a live step-by-step progress view with the same information as the CLI.

## API Deploy

```bash
curl -X POST http://localhost:8800/api/apps/myapp/deploy \
  -H "Content-Type: application/json" \
  -d '{"ref": "abc1234"}'
```

## Pipeline Steps

See [Deploy Pipeline](/v2/architecture/deploy-pipeline) for detailed documentation of each step.

| Step | Description |
|------|-------------|
| validate | Check infraspec shape and reachability assumptions (preflight only) |
| clone | Checkout repo at ref |
| inspect | Check Dockerfile, declared encrypted secrets, and known source footguns (preflight only) |
| build | Build and push Docker image |
| test | Run test command |
| snapshot | pg_dump database |
| migrate | Run database migrations |
| submit | Translate and submit Nomad jobs |
| healthy | Wait for allocations to be healthy |
| forge | Update cloudflared ingress |
| cleanup | Remove temp files |

## Port Conflict Detection

During the **submit** step, norn checks for port conflicts with other running Nomad jobs. If the app's requested static port is already in use by a different job, norn logs a warning with a suggested alternative port:

```
port.conflict: port 8090 is used by mail-indexer — suggest 8092
```

Apps with `endpoints:` in their infraspec use **static ports** (Nomad `ReservedPorts`), meaning the container binds to the exact port specified. If another service already occupies that port on IPv4, Nomad may fall back to IPv6-only binding, which causes cloudflared routing to hit the wrong service.

### Checking used ports

The Nomad client exposes `UsedPorts()` and `SuggestPort(base)`:

- `UsedPorts()` — returns all static ports currently claimed by running jobs
- `SuggestPort(base)` — returns the lowest unused port starting from `base`

To avoid conflicts, pick a port not in use. Currently allocated ports can be queried through the Nomad API or by running `norn status` and inspecting allocations.

## Auto-Deploy

Enable auto-deploy by setting `autoDeploy: true` in the infraspec's repo config and configuring a GitHub webhook.

### GitHub Webhook Setup

1. In your GitHub repo, go to Settings → Webhooks → Add webhook
2. Set the payload URL to `https://norn.example.com/api/webhooks/github`
3. Set the content type to `application/json`
4. Set the secret to match `NORN_WEBHOOK_SECRET`
5. Select "Just the push event"

When a push event is received for an app with `autoDeploy: true`, Norn automatically triggers a deploy with the pushed commit SHA.

## Rollback

```bash
norn rollback <app>
```

Finds the most recent successful deployment and re-deploys its image tag. This skips the clone/build/test steps and goes straight to submit with the previous image.

## Upgrading Norn Itself

When upgrading the Norn control plane, restart only the Norn API LaunchAgent and leave Nomad, Consul, Postgres, and hosted apps running. See [Upgrading Norn](/v2/operations/upgrading) for the safe local upgrade and rollback runbook.
