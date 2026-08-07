# Host Recovery on macOS

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
- Host diagnostics and concise status output.
- Optional bounded cron catch-ups loaded through the encrypted API runtime
  environment.

The existing Norn API and cloudflared launchd jobs remain independently
managed. The host lane does not embed API tokens or app secrets in plists.

## Install

```bash
norn host install --repo /path/to/norn
norn host doctor
```

`install` writes managed configuration and launchd files but deliberately does
not stop live services. Re-running it refreshes the managed script, CLI copy,
configs, and plists while preserving the persistent state directories.

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

`{address}` is replaced with the host's current IPv4 address on every pass, so
Tailscale Serve does not retain a stale DHCP address. Only apps named with
`--required` can be automatically deployed or restarted. Only apps named with
`--forge` have public routes reconciled. Cloudflare reconciliation ignores
`.norn`, `.ts.net`, local, internal, and literal-IP endpoints.

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
norn host status
norn host doctor
norn smoke platform
```

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
running daemon.

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
