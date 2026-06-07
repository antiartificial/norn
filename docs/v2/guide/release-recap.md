---
title: Norn v2 Release Recap
---

# Norn v2 Release Recap

This recap summarizes the current Norn v2 release line: the Nomad/Consul control plane, the operator-facing dashboard and CLI, the ContextDB worker deployment path, and the upgrade posture for the local LaunchAgent install.

## What Shipped

| Area | Surface | Why it matters |
|:-----|:--------|:---------------|
| Runtime platform | Nomad, Consul, local Docker builds, cloudflared | Replaces the v1 Kubernetes path with a smaller local control plane suited to self-hosted apps |
| App model | Multi-process `infraspec.yaml` | Lets one app define web, worker, cron, and function processes without splitting deployment ownership |
| Deploy pipeline | Clone, build, test, snapshot, migrate, submit, healthy, forge, cleanup | Makes deploys repeatable and auditable from the CLI, API, and dashboard |
| Service discovery | `/api/services/manifest` and `norn services` | Gives operators and agents a compact view of hosted services, process reachability, endpoint scope, and health |
| Operations dashboard | Platform and ContextDB ops panels | Surfaces service exposure, deploy provenance, snapshot retention, access events, OTEL/Grafana status, and ContextDB worker posture |
| CLI operations | `norn ops platform`, `norn ops contextdb`, `norn smoke contextdb` | Turns platform health and ContextDB worker readiness into repeatable terminal checks |
| ContextDB worker hosting | Separate `web` and `review-worker` processes | Lets ContextDB run centralized claim review as a durable Norn-managed worker instead of embedding worker logic per application |
| Worker policy checks | `norn contextdb policy`, `review`, `worker-runs`, `evaluator-smoke`, `audit` | Shows dry-run state, evaluator mode, provider readiness, queue posture, run summaries, and feedback audit events before mutation rollout |
| Secrets hygiene | SOPS/age, secret status checks, plaintext env warnings | Keeps secret values out of API/UI responses while making missing or unsafe secret wiring visible |
| Snapshot operations | Snapshot listing, restore confirmation, retention reporting | Adds safer database restore flows and platform-level visibility into snapshot drift and over-limit state |
| Observability | OTEL configuration and slog fan-out | Prepares the API for Grafana-backed logs, traces, and metrics while keeping local logs useful |
| Upgrade path | LaunchAgent-safe upgrade runbook | Upgrades Norn API, CLI, and built UI without stopping Nomad, Consul, Postgres, or hosted apps |

## Operator Impact

Norn v2 is now useful as a real local operations surface rather than just a deploy script. The dashboard and CLI both answer the daily questions: what is hosted, what is healthy, what changed recently, which services are reachable, and whether the platform is safe to upgrade.

The biggest practical change is that Norn can host long-lived background work beside web processes. ContextDB is the proving case: its web API and review worker run as separate processes, while Norn exposes worker health, evaluator readiness, dry-run policy posture, audit events, and recent worker runs.

## Verification

The current release line has been exercised with:

- `cd v2/ui && pnpm build`
- `cd v2 && make build`
- `go test ./...` in `v2/api`
- `go test ./...` in `v2/cli`
- `cd docs && pnpm build`
- `launchctl kickstart -k gui/$(id -u)/com.norn.api`
- `norn version`
- `norn ops platform`
- `norn services`
- `norn status`
- `norn smoke contextdb`
- `curl -sf http://127.0.0.1:8800/api/health`

## Compatibility

Norn v2 is the active development path and is intentionally separate from the v1 Kubernetes documentation. The v2 upgrade path replaces only the Norn API binary, CLI binary, and built UI assets when Norn is installed as `com.norn.api`. It does not require stopping Nomad, Consul, Postgres, or hosted allocations.

## Read Next

- [Getting Started](/v2/guide/getting-started)
- [Next Steps](/v2/guide/next-steps)
- [Deploy Pipeline](/v2/architecture/deploy-pipeline)
- [CLI Commands](/v2/cli/commands)
- [Upgrading Norn](/v2/operations/upgrading)
