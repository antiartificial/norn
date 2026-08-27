# Host Recovery on macOS

Use the host runtime lane to make a local Norn installation survive logout,
reboot, and Docker Desktop restarts without rebuilding every app.

## What it manages

- Persistent Nomad and Consul data outside `/tmp`.
- User launchd jobs for Nomad and Consul.
- A one-shot launchd supervisor that starts Docker, waits for Consul and Nomad,
  validates and recovers the Norn-owned cloudflared LaunchAgent, restarts the
  Norn API after its dependencies are ready, and runs an optional post-recovery
  hook.
- Host diagnostics and concise status output.

The Norn API remains independently managed. The host lane owns cloudflared's
service definition but keeps tunnel credentials in the normal cloudflared
config/credentials files; it does not embed API tokens or app secrets in plists.

## Install

```bash
norn host install --repo /path/to/norn \
  --cloudflared-config ~/.cloudflared/config.yml \
  --cloudflared-probe https://service.example.com/health
norn host cloudflared-recover --cloudflared-probe https://service.example.com/health
norn host doctor --cloudflared-probe https://service.example.com/health
```

`install` writes managed configuration and launchd files but deliberately does
not stop live services.

For a bounded cron process that should run once after recovery, configure one
or more catch-ups at install time:

```bash
norn host install --repo /path/to/norn --catch-up app-name:daily-capture
```

The managed CLI loads the existing encrypted API runtime environment before it
triggers the process. Catch-up failures are logged but do not mark the core host
recovery as failed.

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
norn host status
norn host doctor
norn smoke platform
```

At login, `com.norn.host-supervisor` performs the same ordered recovery. It
re-renders bind and advertise addresses from the current default network route,
so a DHCP address change does not leave the scheduler bound to a stale address.
It also validates cloudflared ingress before loading the managed LaunchAgent.
If a public probe is configured, recovery waits for that route before reporting
success.

For a cloudflared-only recovery after a Homebrew upgrade:

```bash
norn host cloudflared-recover \
  --cloudflared-config ~/.cloudflared/config.yml \
  --cloudflared-probe https://service.example.com/health
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
- Do not use `brew services start/restart cloudflared` for a Norn-managed
  tunnel. Those commands overwrite the Norn service arguments with Homebrew's
  bare-binary service definition.
