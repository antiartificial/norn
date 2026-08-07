# Host Recovery and Assurance on macOS

Use the host runtime lane to make a local Norn installation survive logout,
reboot, and Docker Desktop restarts without rebuilding every app.

## What it manages

- Persistent Nomad and Consul data outside `/tmp`.
- User launchd jobs for Nomad and Consul.
- A one-shot launchd supervisor that starts Docker, waits for Consul and Nomad,
  restarts the Norn API after its dependencies are ready, runs bounded catch-ups,
  and performs an assurance pass before an optional post-recovery hook.
- A periodic assurance LaunchAgent that repairs explicitly required apps and
  routes, then probes the endpoints users actually reach.
- An independent `com.norn.host-agent` process that claims durable, allow-listed
  platform and host maintenance operations from PostgreSQL.
- Host diagnostics and concise status output.
- Optional bounded cron catch-ups loaded through the encrypted API runtime
  environment.

The existing Norn API and cloudflared launchd jobs remain independently
managed. The host lane does not embed API tokens or app secrets in plists.

## Recovery order

The login supervisor and `norn host recover` use the same ordered flow:

1. Re-render Nomad and Consul configuration with the current host IPv4 address.
2. Start Docker and wait for it to accept commands.
3. Start Consul and wait for a leader.
4. Start Nomad and wait for a leader.
5. Restart the Norn API and wait for `/api/health`.
6. Start the host maintenance agent.
7. Trigger configured bounded cron catch-ups.
8. Run host assurance.
9. Run the optional installation-specific `post-recover` hook.

Core recovery completes even when assurance still has a failing endpoint. The
failure is recorded through Beacon, and the periodic assurance agent retries
the idempotent repair pass without repeatedly restarting the core runtime.

## Install

```bash
norn host install --repo /path/to/norn
norn host doctor
```

`install` writes managed configuration and launchd files but deliberately does
not stop live services. Re-running it refreshes the managed scripts, CLI,
`norn-host-agent`, configs, and plists while preserving the persistent state
directories. The host agent loads the existing encrypted API environment at
process start; database credentials are not written into the plist.

For a bounded cron process that should run once after recovery, configure one
or more catch-ups at install time:

```bash
norn host install --repo /path/to/norn --catch-up app-name:daily-capture
```

The managed CLI loads the existing encrypted API runtime environment before it
triggers the process. Catch-up failures are logged but do not mark the core host
recovery as failed.

Configure the assurance stage with an explicit allowlist. Required jobs may be
deployed when absent or restarted when their Consul service is not passing on
IPv4. Public routes are reconciled through Cloudflare, private routes through
Tailscale Serve, and HTTP probes are retried after those repairs:

```bash
norn host install --repo /path/to/norn \
  --required api:web \
  --forge api \
  --serve '8443=http://{address}:8443' \
  --probe api-public=https://api.example.com/health \
  --probe api-tailnet=https://host.example.ts.net:8443/health
```

| Policy | Behavior |
|--------|----------|
| `--required APP:PROCESS` | Require a passing Consul instance on IPv4. Deploy `HEAD` when the app's Nomad job is absent; restart the app when the job exists but remains unhealthy after retries. |
| `--forge APP` | Reconcile the app's public endpoints through cloudflared. Private and literal-IP endpoints are rejected, and stale private ingress rules are pruned. |
| `--serve PORT=TARGET` | Reapply an idempotent Tailscale Serve listener. `{address}` expands to the current detected IPv4 address. |
| `--probe NAME=URL` | Retry an unauthenticated HTTP GET against the actual public or tailnet entrypoint. Redirects are followed and HTTP error responses fail. |
| `--assure-interval SECONDS` | Set the launchd interval. The default is 300 seconds and the minimum is 60. |

Each policy flag is repeatable. Re-running `host install` with a non-empty
policy refreshes the corresponding managed file under
`~/.config/norn/host/`. Existing policy files are preserved when a flag class
is omitted, which makes it safe to refresh the managed binaries and plists
without accidentally clearing host-specific policy.

`{address}` is replaced with the host's current IPv4 address on every pass, so
Tailscale Serve does not retain a stale DHCP address. Only apps named with
`--required` can be automatically deployed or restarted. Only apps named with
`--forge` have public routes reconciled. Cloudflare reconciliation ignores
`.norn`, `.ts.net`, local, internal, and literal-IP endpoints.

Choose probes that exercise dependencies without returning sensitive data. A
good probe is the same route a client uses, backed by a dependency-aware health
handler. For an authenticated product API, expose a narrow unauthenticated
health endpoint rather than placing a bearer token in the assurance policy.

## Assurance behavior

`norn host assure` is safe to run interactively. The same command is invoked by
the recovery supervisor and by `com.norn.host-assurance`.

- A filesystem lock suppresses overlapping passes and recovers from a stale
  lock left by a terminated process.
- Required services are retried before any repair to avoid reacting to a short
  Consul transition.
- Only entries in `--required` authorize deploy or restart actions.
- Route reconciliation runs before endpoint probes.
- HTTP probes retry with bounded backoff.
- A failing pass exits non-zero and emits `host.assurance.failed` through
  Beacon, deduplicated for one hour.
- The first passing run after a failure emits `host.assurance.recovered` with
  the same correlation key so the incident can be resolved automatically.

## One-time state migration

Wait for active deploy operations and important batch allocations to drain.
Then stop the current Nomad and Consul agents and migrate their state:

```bash
norn host migrate-state \
  --from-nomad /path/to/current/nomad-data \
  --from-consul /path/to/current/consul-data
norn host recover
```

`migrate-state` refuses to run while the Nomad or Consul HTTP API is reachable.
It also rejects broad or empty destination paths.

## Recovery and diagnostics

```bash
norn host recover
norn host assure
norn host queue-assure
norn host status
norn host doctor
norn smoke platform
```

`queue-assure` submits `host.assure` through the control API and waits on its
durable receipt. Use the direct `host assure` command for local bootstrap or
when the API/agent lane itself is under repair.

At login, `com.norn.host-supervisor` performs the same ordered recovery. The
assurance stage then checks required allocations and routes before probing the
real HTTP entrypoints. `com.norn.host-assurance` repeats that idempotent pass
every five minutes by default; use `--assure-interval` at install time to set a
different interval of at least 60 seconds. Persistent failures emit a deduped
critical Beacon event, and the first subsequent passing run emits a correlated
recovery event.

The recovery flow
re-renders bind and advertise addresses from the current default network route,
so a DHCP address change does not leave the scheduler bound to a stale address.
The supervisor retries launchd transitions to tolerate agents that are still
finishing a prior stop.

`norn host status` reports each managed service plus the supervisor's last exit
code. A completed one-shot supervisor appears as `ready`, not as a continuously
running daemon. The periodic agent appears as `armed`.

For launchd-level verification, inspect the managed log after a pass:

```bash
tail -100 ~/.local/log/com.norn.host-assurance.log
launchctl print "gui/$(id -u)/com.norn.host-assurance"
```

## Optional post-recovery hook

If this executable exists, the supervisor runs it after Norn is healthy:

```text
~/.config/norn/host/post-recover
```

Use the hook for installation-specific checks such as an authenticated app
smoke test or a bounded, idempotent ingestion catch-up. Keep credentials in the
normal encrypted runtime environment rather than in the hook or launchd plist.

## Limits

- User LaunchAgents run after login. For a machine that must recover before any
  user session exists, use equivalent root LaunchDaemons or move the runtime to
  a Linux VM with systemd.
- Persisted Nomad state restores jobs and their periodic schedules, but a cron
  scheduler does not replay every interval missed during a long outage. Use an
  application-specific post-recovery hook for stale-ingestion catch-up.
- Assurance probes are unauthenticated HTTP GETs. Prefer a dependency-aware
  health endpoint that verifies critical upstreams without exposing data or
  placing credentials in launchd configuration.
