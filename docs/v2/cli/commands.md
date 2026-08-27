# CLI Commands

Full reference for every `norn` command.

## status

List all apps with their health, current commit, endpoints, and services.

```bash
norn status
```

Displays a table of all discovered apps with live health indicators, latest deployment image, and resolved commit.

## runtime

Show the explicitly selected workload connector, image runtime, availability,
capabilities, and enforced limitations.

```bash
norn runtime
```

The command reads the versioned `/api/v1/host/runtime` control endpoint. It is
the quickest way to confirm that a host is using the production
`nomad-consul` connector or the development-only `apple-container` connector.
Selection is server-side through `NORN_WORKLOAD_CONNECTOR`; this command does
not mutate the active runtime.

## app

Detailed view of a single app including processes, object storage buckets, recent deployments, and infrastructure.
The output includes service-manifest reachability, network mode, endpoints, and instances when available.

```bash
norn app <id>
```

| Argument | Description |
|----------|-------------|
| `id` | App name (from infraspec `name` field) |

## deploy

Deploy an app at a specific git ref with live pipeline progress.

```bash
norn deploy <app> [ref]
norn deploy steps <deployment-id>
```

| Argument | Default | Description |
|----------|---------|-------------|
| `app` | — | App name |
| `ref` | `HEAD` | Git ref (commit SHA, branch, tag) |

Connects to the WebSocket and renders a real-time progress display showing each pipeline step. The saga ID is printed on completion for later inspection.

`norn deploy steps <deployment-id>` shows durable deploy or rollback checkpoints from `deployment_steps`.

## preflight

Run a deploy rehearsal without changing Norn runtime state.

```bash
norn preflight <app> [ref]
norn check <app> [ref]
```

| Argument | Default | Description |
|----------|---------|-------------|
| `app` | - | App name |
| `ref` | `HEAD` | Git ref (commit SHA, branch, tag) |

Preflight validates the infraspec, prepares the deploy source tree, checks the configured Dockerfile and declared encrypted secrets, runs a local Docker build, and runs `build.test` when configured. It does not create a deployment record, snapshot a database, run migrations, submit Nomad jobs, wait for health, or update cloudflared.

Warnings are streamed inline for conditions that are worth seeing before deploy, such as `repo.autoDeploy: false` or Go module `replace` directives that point outside the prepared build context.

## platform

Manage the Norn control plane itself with a release-oriented upgrade lane.

```bash
norn platform preflight [ref]
norn platform upgrade [ref]
norn platform upgrade [ref] --proxy
norn platform rebuild <full-sha> --verify
norn platform releases
norn platform rollback <sha-prefix>
norn platform smoke
norn platform env -- <command> [args...]
norn platform proxy-plan
norn platform proxy-status
norn platform proxy-render
norn platform proxy-switch <port|host:port>
norn platform queue-preflight [ref]
norn platform queue-upgrade [ref] --mode restart --drain fail
norn platform queue-rollback <sha-prefix>
norn platform queue-smoke
```

`norn platform preflight` builds Norn from an isolated git worktree into `$HOME/norn/releases/<sha>`, starts the candidate API on `127.0.0.1:18800`, and verifies health/version without restarting the active API.

`norn platform upgrade` performs the same preflight, promotes `$HOME/norn/current`, installs compatibility binaries into `$HOME/go/bin`, restarts only `com.norn.api`, and rolls back to the previous release if postflight health fails.

`norn platform upgrade --proxy` uses the managed proxy lane. It boots the candidate API on the private candidate port, switches the managed Caddy upstream, keeps the new API alive behind the proxy, and stops the previous proxy-managed API pid after postflight succeeds. Use it only after the host is intentionally proxy-fronted and `NORN_PROXY_RELOAD=true` is configured.

`norn platform rebuild <full-sha> --verify` delegates to the managed immutable-release lane. It rejects abbreviated or non-hex refs and refuses to run without `--verify`; the selected artifact must be bound to that full commit SHA and pass its checksum and signature verification before it is rebuilt. This is an operator recovery command, not a command to publish GitHub release assets.

`norn platform releases` lists local release directories. `norn platform rollback <sha-prefix>` promotes a previous local release and runs the same postflight health check. Roll back locally first; if it is absent, the managed script may invoke a configured `NORN_RELEASE_FETCH_HOOK` to restore the artifact into the release store before verification. The hook is a recovery integration, not a general CLI publish command. A binary rollback does not roll back database schema: every release must remain compatible with the active database schema until a separate, tested data migration/recovery plan is complete.

For GitHub Release recovery, configure the host script rather than adding
publication credentials to the CLI:

```bash
NORN_RELEASE_FETCH_HOOK=platform-release-fetch-github
NORN_RELEASE_VERIFY_HOOK=platform-release-verify-github
NORN_RELEASE_REPOSITORY=antiartificial/norn
NORN_RELEASE_PUBLIC_KEY=/secure/path/norn-release.pub
NORN_RELEASE_SIGNATURE_POLICY=require-signed
# Public antiartificial/norn: no token. Private mirror: use an owned,
# non-symlink, exact mode 0600
# NORN_RELEASE_GITHUB_TOKEN_FILE (env token and GH_TOKEN remain supported).
```

The adapters receive `<sha-or-prefix> <releases-dir>` for fetch and
`<release-dir> <release.json>` for verification. They resolve the
GitHub Release tagged `platform-<fullsha>` and accept only the matching
`norn-platform-<fullsha>-<os>-<arch>.{tar.gz,manifest.json,manifest.sig,sbom.spdx.json}`
asset set. Configure the repository, public verification key, and a read-only
GitHub release token file through the host's protected runtime environment when
the repository is private. Never
place provider credentials or release-signing keys on a Norn host, and do not
use the general operator CLI to package, sign, or publish assets.

`norn platform smoke` runs `norn smoke platform` with the API runtime environment loaded from the encrypted SOPS JSON env file.

`norn platform env -- <command>` runs an arbitrary command with that same API runtime environment loaded without printing secret values.

`norn platform proxy-plan` prints a no-blip reverse-proxy cutover plan. `proxy-status`, `proxy-render`, and `proxy-switch` manage an optional local Caddy config and upstream state file. They do not change the live topology unless explicitly invoked.

The `queue-*` commands use the authenticated v1 control protocol. They create
durable operations, wait by default, and are executed by the independent host
agent, so `queue-upgrade` can remain in progress across the API restart it
causes. Pass `--wait=false` to return after enqueueing and inspect the receipt
with `norn operations <operation-id>`.

| Flag | Default | Description |
|------|---------|-------------|
| `--repo` | `NORN_PLATFORM_REPO` | Norn checkout containing `v2/scripts/platform-upgrade` |
| `--script` | `NORN_PLATFORM_SCRIPT` | Explicit platform-upgrade script path |
| `--proxy` | `false` | Use managed proxy cutover mode for `platform upgrade` |

## host

Install, recover, assure, and diagnose a persistent macOS Norn runtime.

```bash
norn host install --repo /path/to/norn
norn host migrate-state \
  --from-nomad /path/to/current/nomad-data \
  --from-consul /path/to/current/consul-data
norn host recover
norn host cloudflared-recover \
  --cloudflared-config ~/.cloudflared/config.yml \
  --cloudflared-probe https://service.example.com/health
norn host assure
norn host queue-assure
norn host status
norn host doctor
norn host prerequisites --connector nomad-consul --check
norn host setup --connector apple-container
norn host security plan
norn host security init --address 10.0.0.10 --cert-days 365
```

`host install` writes managed Nomad and Consul configs, persistent state
directories, and user LaunchAgents without interrupting live agents. It can
also configure a bounded post-recovery cron trigger:

```bash
norn host install --repo /path/to/norn --catch-up app-name:daily-capture
```

Run `norn host prerequisites` before `host install` on a new machine. It is a
non-mutating base-system check and deliberately does not require generated
plists or a running Norn API. `nomad-consul` is the default and only
production-supported connector; it checks Docker, Nomad, Consul, SOPS, and
host tooling. Its installation remains a deliberate infrastructure procedure
because TLS, ACLs, Docker administration, and persistent state need an
operator-approved configuration.

For local Apple-silicon macOS development, `norn host setup --connector
apple-container` offers the narrow automated path. It confirms macOS 26+ and
arm64, asks before each change, downloads a version-pinned package only from
the official `apple/container` GitHub release, verifies the local Apple
Containerization Developer ID signature, notarization, and Gatekeeper result,
then invokes macOS Installer and starts the runtime. It never pipes a network
response into a shell. Use `--check` to inspect only, `--dry-run` to preview,
and `--yes --non-interactive` for approved automation (macOS sudo credentials
are still required for package installation).

The install command also accepts an explicit assurance policy:

```bash
norn host install --repo /path/to/norn \
  --required api:web \
  --forge api \
  --serve '8443=http://{address}:8443' \
  --probe api-public=https://api.example.com/health \
  --assure-interval 300
```

| Flag | Meaning |
|------|---------|
| `--required APP:PROCESS` | Require a passing IPv4 Consul service; deploy an absent app or restart an unhealthy app |
| `--forge APP` | Reconcile the app's public Cloudflare routes and prune stale private ingress rules |
| `--serve PORT=TARGET` | Reconcile a Tailscale Serve listener; `{address}` expands to the current host IPv4 address |
| `--probe NAME=URL` | Retry an unauthenticated HTTP GET against the route users actually reach |
| `--assure-interval SECONDS` | Set the periodic assurance interval; minimum 60, default 300 |
| `--cloudflared-config PATH` | Set the named-tunnel config managed by Norn |
| `--cloudflared-label LABEL` | Set the Norn-owned LaunchAgent label; default `com.norn.cloudflared` |
| `--cloudflared-probe URL` | Check a public health URL after tunnel recovery |
| `--skip-cloudflared` | Leave cloudflared outside host management |
| `--security-dir PATH` | Set the staged/active host security root |
| `--cert-days DAYS` | Set staged leaf-certificate validity; minimum 30 |
| `--nomad-security-fragment PATH` | Reserved; authenticated probes are supported, but activation waits for atomic fleet token/TLS cutover and rollback |
| `--consul-security-fragment PATH` | Reserved; authenticated probes are supported, but activation waits for atomic fleet token/TLS cutover and rollback |

Flags that define policy are repeatable. Only explicitly required apps may be
deployed or restarted. `norn host assure` runs the same idempotent pass used by
the login supervisor and the periodic `com.norn.host-assurance` LaunchAgent.
Persistent failures emit a deduplicated critical Beacon event; the first
subsequent passing run emits a correlated recovery event.

`host migrate-state` is the one-time cutover command. It refuses to copy state
while the Nomad or Consul HTTP API remains reachable. `host recover` starts
Docker, Consul, Nomad, and the Norn API in dependency order, runs configured
catch-ups, validates and recovers cloudflared, and invokes assurance. The
targeted `host cloudflared-recover` command is safe after a Homebrew or macOS
update; do not use `brew services restart cloudflared`. `host assure` verifies and repairs the
explicit policy without restarting the core host runtime. `status` is the
compact operator view and `doctor` validates tools, plists, persistence, and
runtime health.

See [Host Recovery](/v2/operations/host-recovery) for the full migration and
failure-recovery procedure.

`host security init` creates an inactive, timestamped private PKI stage and
transition fragments. It never restarts agents or bootstraps ACL tokens.
`host security plan` shows value-safe activation state and the required cutover
sequence. Managed activation is unavailable in this release. See [Production
Readiness](/v2/operations/production-readiness).

## production

Evaluate the authenticated production-admission contract:

```bash
norn production check
norn production check --json
norn production audit
norn production audit --limit 200
norn production drills
norn production drill start database.restore --target restore-sandbox
norn production drill complete <id> --status passed --evidence snapshot=object-version-id --evidence rto=4m12s
```

The command exits non-zero while a required gate fails. JSON output uses
`norn.production-readiness/v1` and is suitable for CI, change approvals, and
native clients. Enabling `NORN_PROFILE=production` additionally makes API
startup, live substrate mutation admission, immutable registry resolution,
external PostgreSQL/PITR/replica checks, integrity-signed mutation audit, and
90-day recovery-drill freshness fail closed. Drill evidence is bounded receipt
metadata; keep full logs in durable object storage.

## operations

List durable operation records.

```bash
norn operations
norn operations --active
norn operations <operation-id>
```

Operations summarize long-running app, platform, and host work. Use `--active`
before invasive platform work; pass an operation ID to retrieve its current
lease state and final receipt.

| Flag | Default | Description |
|------|---------|-------------|
| `--active` | `false` | Only show queued/running operations |
| `--limit` | `25` | Maximum operations to show |

## operator

Show the operator-confidence release surfaces.

```bash
norn operator inbox
norn operator cron
norn operator wake-targets
norn operator deploy-confidence
norn operator snapshot-readiness
norn operator auth-hints
norn operator actions
```

`norn operator inbox` is the high-signal entry point. It combines active
incidents, active or failed durable operations, deploy-confidence warnings,
cron risks, snapshot readiness, secret status, and wake target counts into one
recommended-action list.

| Command | Purpose |
| --- | --- |
| `inbox` | Show recommended operator actions across the platform |
| `cron` | Show schedules, local next/last run times, Nomad child counts, and cron risk |
| `wake-targets` | Show endpoint readiness and wake-gateway URLs |
| `deploy-confidence` | Show recent deploy health, auto-rollback, canary, and preflight guidance |
| `snapshot-readiness` | Show local restore points, retention overages, and remote export readiness |
| `auth-hints` | Show secret-safe operational authentication patterns |
| `actions` | Show mobile-ready action descriptors and risk levels |

## events

Show recent Norn Beacon events.

```bash
norn events
norn events --severity critical
norn events --app contextdb --limit 10
norn events show <event-id>
norn events ack <event-id> --note "investigating"
norn events snooze <event-id> --for 2h
norn events open <event-id>
norn events reconcile --dry-run
norn events reconcile --app contextdb --limit 50 --by operator
```

| Flag | Default | Description |
|------|---------|-------------|
| `--app` | — | Filter events by app |
| `--type` | — | Filter events by type |
| `--severity` | — | Filter events by severity |
| `--limit` | `25` | Maximum events to show |

Events include `open`, `snoozed`, or `acknowledged` state. Detail output prints related metadata such as saga, deployment, operation, process, service, or job ids when Norn recorded them.

`events reconcile` reviews open warning and critical events against later
durable events plus current Nomad or Consul evidence. Run it with `--dry-run`
first. A non-dry-run pass acknowledges only deterministic recoveries and leaves
inconclusive events marked `needs_review`; it never relies on message-string
matching. See [Beacon Events](/v2/operations/beacon#evidence-based-reconciliation)
for the supported event families and proof rules.

## observability

Generate Norn's Prometheus and Grafana starter bundle.

```bash
norn observability bundle
norn observability bundle --out ./norn-observability
norn observability install
norn observability install --overwrite
```

The bundle includes Prometheus scrape config, Prometheus alert rules, a Grafana datasource, a starter dashboard, and suggested Norn service specs for Prometheus, Grafana, and cAdvisor. The default retention target is 30 days or 8GB.

`norn observability install` writes generated Norn app directories into `NORN_APPS_DIR`: `norn-prometheus`, `norn-grafana`, and `norn-cadvisor`. Review ports and host policy, then validate, preflight, and deploy them like normal Norn apps.

## network

Summarize service reachability and network-mode guidance.

```bash
norn network
```

The command combines `/api/services/manifest` with validation hints to show service exposure, endpoint scope, instance scope, and guidance for `local`, `tailnet`, or `public` mode.

## alerts

Show built-in alert rules derived from Beacon event types.

```bash
norn alerts
```

The rule catalogue is intentionally declarative. It gives CLI, UI, and downstream sinks a shared contract for deploy failure, service down/degraded, cron failure, and recovery events.

## resources

Show live memory usage against declared infraspec limits and print right-sizing suggestions.

```bash
norn resources
```

The command calls `/api/resources/suggestions` and compares Nomad allocation stats with each process's declared `resources.memory` value.

| Status | Meaning |
|--------|---------|
| `at_risk` | Live or peak memory is close to the declared limit; consider increasing `resources.memory` |
| `overprovisioned` | Declared memory is much higher than observed use; the limit may be reducible |
| `right_sized` | Live usage is comfortably inside the declared limit |

Use this after restart loops, OOM events, or a new workload rollout to decide whether an app spec needs resource changes before the next deploy.

## tune

Show advisory CPU, memory, and scale recommendations from live tuning signals.

```bash
norn tune
norn tune recommend
norn tune status
```

The command calls `/api/tuning/recommendations`. It uses live Nomad allocation signals by default, includes any process-level `tuning.signals` declarations from `infraspec.yaml`, and folds in hosted-service access patterns when `/api/access/observations` has data. Recommendations are advisory only: Norn reports the suggested target state but does not update a job or rewrite an app spec.

| Field | Meaning |
|-------|---------|
| `current` | Declared CPU, memory, and observed running allocation count |
| `recommended` | Advisory CPU, memory, and scale target after applying thresholds and `tuning.limits` |
| `signals` | Live or declared signals that informed the recommendation |
| `confidence` | `low` when only current live data is available; higher confidence is reserved for historical signal support |

Access-pattern signals add `observe_access` or `candidate_idle` actions. `observe_access` means the service has no access observations in the lookback window, so Norn needs traffic data before recommending scale-to-zero. `candidate_idle` means access was observed previously, but the service has been quiet beyond the idle threshold.

## notifications

Manage Beacon notification channels.

```bash
norn notifications list
norn notifications add discord ops https://discord.com/api/webhooks/...
norn notifications add ntfy alerts https://ntfy.sh/norn-alerts --severity warning,critical
norn notifications add pushover mobile https://api.pushover.net/1/messages.json \
  --token <app-token> --user-key <user-key> --severity critical
norn notifications add webhook vigil https://vigil.example.com/api/events --severity critical
norn notifications test <channel-id>
norn notifications remove <channel-id>
```

Supported providers are `discord`, `ntfy`, `pushover`, and `webhook`. Severity filters are optional; when omitted, the channel receives all Beacon severities. Notification channel configuration is stored by the Norn control plane and managed through `/api/notifications/channels`.

## smoke

Run operational smoke checks.

```bash
norn smoke platform
norn smoke contextdb
```

`norn smoke platform` checks API health, platform rollup, active operation drain, current release marker, and recent warning/critical Beacon events. It requires authenticated API access when the platform is protected.

Use `norn platform smoke` on hosts where the API token lives in the encrypted runtime env rather than the interactive shell.

## webhooks

List recent webhook deliveries.

```bash
norn webhooks
norn webhooks --limit 50
norn webhooks replay <delivery-id>
norn webhooks replay <delivery-id> --preflight
```

The webhook inbox shows delivery status, matched app, branch, and ignored or failed reason for GitHub and Gitea webhook deliveries.

`norn webhooks replay` queues a delivery again through the durable operation queue. Use `--preflight` to run the same matched app/ref through the read-only preflight lane instead of deploying it.

## restart

Replace every active allocation for an app.

```bash
norn restart <app>
```

Norn stops allocations whose desired status is still `run`, causing Nomad to
reschedule them from the unchanged job definition. This creates fresh task,
network, and port-binding state instead of merely evaluating an unchanged job.
The command returns after Nomad accepts the replacement requests; use the app
health or service manifest to follow the new allocations to readiness.

## rollback

Rollback to the previous successful deployment.

```bash
norn rollback <app>
```

Finds the last successful deployment and re-deploys its image tag.

## canary

Inspect and promote active canary deployments.

```bash
norn canary <app>
norn promote <app>
```

`norn canary <app>` prints the latest Nomad deployment id, job id, deployment status, status description, and whether the active deployment still has canary allocations. `norn promote <app>` promotes the canary deployment through Nomad and emits a Beacon event.

Canary behavior is declared per process with `canary.count` and `canary.evaluateAfter` in `infraspec.yaml`. During deploy, Norn waits for the configured evaluation window after the healthy step, checks allocation health, then promotes or fails the deployment.

## deploy groups

List and run ordered multi-app deployment groups.

```bash
norn deploy-groups
norn deploy-group <name> [ref]
```

Deploy group definitions live under `deploy-groups/*.yaml` and list apps in the order they should roll out. Each app entry may request a `waitReady` gate so Norn waits for health before moving to the next app.

`norn deploy-groups` shows configured groups, apps, and wait-ready settings. `norn deploy-group <name> [ref]` starts each app deploy in order and prints the saga id or error for each app.

## scale

Scale a specific task group to a given count.

```bash
norn scale <app> <group> <count>
```

| Argument | Description |
|----------|-------------|
| `app` | App name |
| `group` | Task group / process name |
| `count` | Target instance count |

::: info
Nomad's `Jobs().Scale()` takes `*int`, not `*int64`.
:::

## logs

Stream live logs from a running app.

```bash
norn logs <app>
```

Opens a fullscreen, scrollable log viewer (Bubble Tea TUI). Press `q` or `Ctrl+C` to exit.

## exec

Run a command inside a running allocation. Use `--process` for multi-process apps so Norn targets the intended task group.

```bash
norn exec contextdb --process review-worker -- \
  /contextdb worker review --namespaces hermes-agent --mode agent_memory --dry-run --smoke-evaluator --report
```

## smoke

Run app-specific operational smoke checks.

```bash
norn smoke contextdb
```

`norn smoke contextdb` discovers ContextDB web and review-worker reachability from the service manifest, validates the infraspec, checks web and worker health, writes and retrieves a low-confidence smoke claim, verifies the review queue, runs the review worker in dry-run mode, and checks the resulting worker-run receipt.

| Flag | Default | Description |
|------|---------|-------------|
| `--namespace` | `norn-smoke-<timestamp>` | ContextDB namespace used for the smoke claim |
| `--mode` | `agent_memory` | ContextDB write/retrieve/review mode |
| `--web-url` | manifest endpoint | Override ContextDB web URL |
| `--worker-url` | manifest instance | Override ContextDB review worker health URL |
| `--low-confidence-threshold` | `0.35` | Threshold used when checking the review queue |

## contextdb

Inspect ContextDB-specific integration state from Norn.

```bash
norn contextdb review
norn contextdb policy
norn contextdb policy --json
norn contextdb audit
norn contextdb evaluator-smoke
norn contextdb evaluator-readiness
norn contextdb evaluator-readiness --json
norn contextdb rollback-feedback <event-id> --reason "bad feedback"
norn contextdb review --namespace hermes-agent
norn contextdb worker-runs <namespace>
norn contextdb worker-runs <namespace> --decisions
norn contextdb worker-runs <namespace> --json
```

`norn contextdb review` summarizes the review queue and recent worker runs for a namespace. It defaults to `hermes-agent` in `agent_memory` mode.

`norn contextdb policy` discovers the review worker instance from the service manifest and reads its live `/v1/status` policy report. The report is value-safe: it shows dry-run state, policy preset, evaluator type, whether provider keys are required/configured, allowed actions, mutation status, warnings, and errors without exposing secret values.

`norn contextdb audit` reads recent feedback events from ContextDB so operators can inspect claim validation, refutation, stale marking, and worker-applied mutation receipts.

`norn contextdb evaluator-smoke` runs the deployed review worker's configured evaluator smoke test inside the `review-worker` allocation. It does not open the database or mutate claims; provider-backed evaluators use their configured provider/webhook and report missing keys, policy blocks, malformed decisions, or rate-limit failures before rollout.

`norn contextdb evaluator-readiness` synthesizes policy, key availability, dry-run state, and evaluator configuration into a per-namespace readiness assessment. Use it before moving a namespace from the rules evaluator or dry-run mode to a provider-backed evaluator.

`norn contextdb rollback-feedback` proxies a ContextDB feedback rollback through the hosted web service and prints the rollback receipt. Use the feedback event id from `norn contextdb audit`, `norn ops contextdb`, or the Ops UI.

`norn contextdb worker-runs` discovers the ContextDB web endpoint from the service manifest and lists durable review worker summaries for a namespace. The table includes generated time, cycle id, mode, evaluator, dry-run flag, scanned/applied/skipped/error counts, and decision count. Use `--decisions` to include each decision's type, action, applied flag, node id, and reason.

| Flag | Default | Description |
|------|---------|-------------|
| `--mode` | `agent_memory` | ContextDB mode |
| `--after` | — | Only show runs after this RFC3339 timestamp |
| `--limit` | `10` | Maximum runs to show after fetching |
| `--decisions` | `false` | Print decision details below each run |
| `--json` | `false` | Print raw JSON |
| `--web-url` | manifest endpoint | Override ContextDB web URL |

## ops

Show operator rollups for hosted services.

```bash
norn ops platform
norn ops contextdb
```

`norn ops platform` calls Norn's platform operations endpoint and summarizes service exposure, recent deployment provenance, dirty local builds, secret hygiene, snapshot retention state, recent access status buckets, and OpenTelemetry/Grafana configuration.

`norn ops contextdb` calls Norn's ContextDB operations endpoint and summarizes app health, web/worker reachability, value-safe worker policy, provider rollout gate, review queue size, recent worker runs, recent feedback audit events, snapshots, secrets, and recent deployments.

## health

Check the health of all backing services (Nomad, Consul, PostgreSQL, and configured S3-compatible object storage such as Garage).

```bash
norn health
# alias:
norn doctor
```

Displays a checklist of service statuses with pass/fail indicators.
The output also shows the configured Norn network mode from `NORN_NETWORK_MODE`.

## metrics

Norn exposes Prometheus-compatible metrics over HTTP rather than a CLI command:

```bash
curl http://127.0.0.1:8800/metrics
curl http://127.0.0.1:8800/api/observability/prometheus.yml
```

With explicit authentication enabled, pass a scoped bearer token using an
`Authorization` header. For Prometheus, prefer a permission-restricted
`bearer_token_file` rather than embedding the token in YAML or shell history.

The generated Prometheus config includes Norn itself and any app process that declares `metrics.enabled: true`.

## stats

Display deployment and cluster statistics.

```bash
norn stats
```

Shows total apps, recent deployments, active allocations, and other cluster metrics.

## access

Show recent Norn API access events.

```bash
norn access
norn access --limit 100
norn access patterns --window 14d --idle-after 7d
norn access observe myapp --process web --endpoint https://app.example.com --source gateway --status 200
norn access cloudflare status
norn access cloudflare sync --window 14d
norn access enrollments --status pending
norn access approve ABCD-EFGH --scope api:read,events:read
norn access devices
norn access revoke-device <device-id> --confirm
```

The table includes request time, status, method, path, client IP, Cloudflare Access user metadata when present, and duration. Norn does not expose request bodies, authorization headers, or secret values in this view.

`norn access patterns` summarizes durable hosted-service access observations by app and process. It reports request totals, last observed access, quiet duration, peak UTC hour, idle-candidate action, and confidence. The data comes from hourly aggregate buckets, not raw request logs.

`norn access observe` records an aggregate observation for a hosted service. It is intended for the wake gateway, reverse proxies, cloudflared log shippers, or small operator scripts. Observations include app, process, endpoint/source labels, status bucket, count, and optional timestamp; they do not include request bodies or credentials.

`norn access cloudflare status` reports whether Cloudflare GraphQL sync and Logpush receiver secrets are configured, and shows the public service hostnames Norn can map back to app/process pairs.

`norn access cloudflare sync` imports hourly request observations from Cloudflare's GraphQL Analytics API for each mapped public hostname. The sync requires `NORN_CLOUDFLARE_API_TOKEN` and `NORN_CLOUDFLARE_ZONE_ID`. The token should have read access to zone analytics for the target zone. Imported observations are stored as hourly aggregates with source `cloudflare-graphql`.

The sync chunks long windows into day-sized Cloudflare queries and clamps the effective lookback to the configured GraphQL retention ceiling. GraphQL aggregate buckets are replaced on conflict, which makes repeated syncs safe after timeouts or partial imports.

Native device enrollment is managed under the same command group. The Mac app
creates a ten-minute pairing request and shows a code. An authenticated
administrator reviews the requested permissions and approves all of them or an
explicit subset:

```bash
norn access enrollments --status pending
norn access approve ABCD-EFGH --scope api:read,events:read
```

`access enrollments` defaults to pending requests; pass an empty `--status` to
list all recent states. Approval requires `admin`, trusted HTTPS (or direct
loopback), and scopes that were requested by the device. Enrollment never
grants `admin`. The device exchanges the approval automatically and receives a
revocable 30-day credential.

Inventory and revocation are also administrator actions:

```bash
norn access devices
norn access revoke-device 7b46f2c4-8e17-4a50-a914-2d519d34b3ae --confirm
```

Revoking a device also revokes all managed tokens and cancels affected exec
sessions. `--confirm` is required because the action cannot be undone; the
device must enroll again. Device listings contain token metadata but never
bearer values.

The Logpush receiver is `POST /api/access/cloudflare/logpush`. It requires `NORN_CLOUDFLARE_LOGPUSH_TOKEN` and accepts the token in `X-Norn-Logpush-Token`, `X-Logpush-Secret`, or a bearer header. Configure Cloudflare HTTP Logpush to send HTTP request logs to this endpoint over HTTPS with a secret header. Imported observations are stored with source `cloudflare-logpush`.

The wake gateway can record live accesses and wake a scaled-down service on demand. For production, point each wakeable public hostname at the Norn API origin, usually `http://127.0.0.1:8800` on the Norn host, and preserve the original `Host` header. cloudflared hostname ingress preserves this automatically. Generic proxies should pass `Host`, `X-Forwarded-Host`, `X-Forwarded-Proto`, and `X-Forwarded-For`. The local smoke test is:

```bash
curl -i -H "Host: app.example.com" http://127.0.0.1:8800/health
```

For path-based testing, use `GET /api/wake-gateway/<public-hostname>/<original-path>`. A gateway-handled response includes `X-Norn-Wake-Gateway: true` and `X-Norn-Wake-Action: ready` or `scaled`.

The advisory tuner consumes these access patterns. A service with no observations in the lookback window is marked `observe_before_idle`; a service whose last access is older than `--idle-after` is marked `consider_idle`.

Temporary IP grants are managed under the same command group:

```bash
norn access grant --ip 1.2.3.4 --ttl 24h --note "CI server"
norn access grants
norn access revoke <grant-id>
```

Grants bypass bearer auth for a single IP until their TTL expires. Use them for short-lived operator or automation access, and prefer the narrowest practical TTL. The dashboard Platform tab exposes the same grant list/create/revoke flow via `/api/access/grants`.

## secrets

Manage SOPS-encrypted secrets for an app.

```bash
# List secret keys
norn secrets <app>

# Compare declared, encrypted, and plaintext secret state
norn secrets status
norn secrets status <app>

# Set a secret
norn secrets set <app> KEY=VALUE

# Delete a secret
norn secrets delete <app> KEY

# Generate migration commands for plaintext env secrets
norn secrets migrate
norn secrets migrate <app>
norn secrets migrate <app> --apply --apps-dir ~/projects
```

| Subcommand | Description |
|------------|-------------|
| (none) | List secret key names (values are not shown) |
| `status` | Show declared-vs-encrypted drift and plaintext env warnings |
| `migrate-plan` | Show value-safe plaintext env entries that should move to `secrets.enc.yaml` |
| `migrate` | Generate SOPS commands for plaintext env secrets; with `--apply`, update infraspec files by moving keys from `env` to `secrets` |
| `set` | Set or update a secret key-value pair |
| `delete` | Remove a secret |

`norn secrets migrate` is intentionally two-phase. Dry-run prints the affected keys and SOPS commands without writing files. `--apply` edits `infraspec.yaml`, but you still run the generated SOPS commands manually so secret values never pass through the API or docs output.

## services

Inspect the service manifest used by agents, dashboards, and external tooling to answer what Norn is hosting.

```bash
# Human-readable table
norn services

# Raw JSON contract
norn services manifest
```

The table separates app-level endpoints from process reachability. Service processes can list public or local endpoints; worker, cron, and function entries expose process type, status, health path, instances, network mode, and reachability metadata without inheriting unrelated app endpoints. The `REACH` column summarizes endpoint and instance scope, for example `local`, `public/private`, or `internal/local`.

## snapshots

Manage PostgreSQL database snapshots.

```bash
# List snapshots
norn snapshots <app>

# Legacy synchronous restore by a unique compact UTC timestamp
norn snapshots <app> restore <timestamp> --yes
norn snapshots <app> restore <timestamp> --yes --pre-restore

# Preview retention
norn snapshots <app> retention --keep 3
norn snapshots <app> retention

# Execute retention
norn snapshots <app> retention --keep 3 --execute --yes

# Remote export/import
norn snapshots export <app>
norn snapshots remote <app>
norn snapshots import <app> snapshots/<app>/<filename>.dump
```

| Subcommand | Description |
|------------|-------------|
| (none) | List available snapshots with timestamps, source commit, created time, size, and filename |
| `restore` | Restore through the legacy synchronous route using a compact UTC timestamp that matches exactly one inventory entry; requires `--yes` and prints a restore receipt. `--pre-restore` creates a fresh snapshot before the restore. Prefer the versioned `/api/v1` control route or web/native clients for a durable exact-filename restore |
| `retention` | Preview newest-N retention without deleting snapshots; defaults to `snapshots.keep` from the app spec or 3; add `--execute --yes` to prune and print a receipt |
| `export` | Upload the latest local snapshot to the app's configured `snapshots.exportBucket` |
| `remote` | List remote snapshots in the configured export bucket |
| `import` | Download a remote snapshot key back into the local snapshots directory |

## cron

Manage cron (periodic batch) jobs.

```bash
norn cron <app> [subcommand]
```

| Subcommand | Description |
|------------|-------------|
| (none) | Show cron status and schedule |
| `trigger` | Manually trigger a cron job immediately |
| `pause` | Pause a periodic job |
| `resume` | Resume a paused periodic job |
| `schedule <expr>` | Update the cron expression |

## invoke

Invoke a function process.

```bash
norn invoke <app> --process=<name> --body='{"key":"value"}'
```

| Flag | Short | Description |
|------|-------|-------------|
| `--process` | `-p` | Process name to invoke (required) |
| `--body` | `-b` | JSON body or `@file` to read from file |

Returns the execution ID and job ID. The function runs as a one-shot Nomad batch job.

## saga

View the saga event log.

```bash
# View events for a specific saga
norn saga <saga-id>

# Recent events filtered by app
norn saga --app=myapp --limit=50
```

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--app` | `-a` | — | Filter events by app name |
| `--limit` | `-l` | `20` | Maximum number of events to show |

## validate

Validate infraspec files for syntax and configuration errors.

```bash
# Validate a single app
norn validate <app>

# Validate all discovered apps
norn validate

# Treat plaintext secret-like env values as errors
norn validate --strict-secrets
```

Reports errors and warnings for each infraspec field. Validation warns when secret-like values such as DSNs, passwords, tokens, API keys, or client secrets appear in plain `env` blocks. Move those values to `secrets.enc.yaml` and list the key under `secrets`. Add `--strict-secrets`, or set `NORN_STRICT_SECRETS=true` for deploy/preflight validation, to make plaintext secret-like env values fail the gate. Validation also uses `NORN_NETWORK_MODE` to warn when endpoint hosts look mismatched for the active mode, such as localhost endpoints in `tailnet` or `public` mode.

Validate an uploaded file strictly, including unknown YAML fields, and optionally cross-check its logical pool references:

```bash
norn validate --file ./infraspec.yaml --fleet ../norn-fleet/environments/production/nyc3/cluster.yaml
```

## fleet

Inspect desired GitOps capacity and create planning-only receipts. Provider credentials stay in the protected infrastructure runner.

```bash
norn fleet pools
norn fleet validate <cluster.yaml>
norn fleet plan <pool> [--desired N] [--size SLUG] [--strategy blueGreen|rolling] [--reason TEXT]
norn fleet replace <pool> --size SLUG [--reason TEXT]
norn fleet reconcile <pool> [--reason TEXT]
norn fleet checkpoints <plan-id>
norn fleet attempts <plan-id>
norn fleet attempt start <plan-id> --runner-id RUN_ID --commit COMMIT_SHA --plan-sha256 PLAN_SHA [--workflow-url HTTPS_URL] [--heartbeat-timeout 120]
norn fleet attempt heartbeat <plan-id> <attempt-id> --phase PHASE --sequence N --revision N [--message TEXT]
norn fleet attempt advance <plan-id> <attempt-id> --phase PHASE --revision N
norn fleet attempt retry <plan-id> <attempt-id> --runner-id NEW_RUN_ID --reason TEXT --revision N [--workflow-url HTTPS_URL]
norn fleet attempt cancel <plan-id> <attempt-id> --reason TEXT --revision N
norn fleet github status
norn fleet github pr <plan-id>
norn fleet github apply <plan-id> [--allow-destructive]
```

Initial cloud-runner setup lives in the private `norn-fleet` checkout:

```bash
./scripts/setup                 # interactive setup/readiness assistant
./scripts/setup doctor          # secret-safe prerequisite check
./scripts/setup scale app --desired 4
```

Norn owns validation, durable plans, inventory, enrollment/readiness, and
receipts. The protected runner keeps provider and remote-state credentials; the
setup assistant sends those values directly to GitHub environment secrets and
does not copy them into Norn.

The GitHub commands use the Norn server's repository-restricted GitHub App.
`pr` creates or recovers a deterministic branch and review. After that review
merges and its protected main-branch plan succeeds, `apply` discovers the
matching run/artifact and creates or recovers the plan-named protected apply.
Norn never receives provider or remote-state credentials.

Capacity plans are stored as completed `fleet.capacity-plan` operations and do not call a cloud provider. `replace` requires `--size` and forces blue/green planning. The infrastructure repository must enforce its own reviewed-SHA and apply authorization gate. The current private repository uses protected-branch-only environments, strict pull-request checks, reviewed-plan SHA binding, and manual dispatch because its GitHub plan does not provide environment required reviewers.

`checkpoints` shows the append-only runner phases for an applied plan, including provider state serials and evidence digests. It is the operator-facing view of interrupted apply recovery and refuses to combine checkpoints from different commit/plan bindings.

`attempts` shows numbered protected-runner executions, their durable phase,
last heartbeat, revision, and external runner identity. `attempt start` binds an
external run to the reviewed commit and plan digest. `heartbeat` is monotonic;
`advance` fails unless the same attempt has successful evidence for its current
phase. `retry` is valid only for failed or heartbeat-abandoned work and creates
a new attempt without rewriting history. `cancel` cancels Norn's liveness
record only; automation must stop the corresponding external workflow as part
of the same operator action. These mutations require `fleet:operate`;
`api:write` is accepted temporarily for existing runner tokens.

## endpoints

List an app's configured endpoints with their cloudflared status.

```bash
norn endpoints <app>
```

Output shows each endpoint with a status indicator:

```
endpoints for myapp

  ● app.example.com    active
  ○ api.example.com    inactive
```

- `●` active — hostname is routed in cloudflared
- `○` inactive — hostname is not in cloudflared ingress
- `?` unknown — cloudflared config is unavailable (dev mode)

### endpoints toggle

Toggle a single endpoint on or off in cloudflared.

```bash
norn endpoints toggle <app> <hostname>
```

| Argument | Description |
|----------|-------------|
| `app` | App name |
| `hostname` | The hostname to toggle (e.g. `app.example.com`) |

Determines the current state from the cloudflared ingress list and flips it. If the endpoint is active, it will be disabled; if inactive, it will be enabled.

```
$ norn endpoints toggle myapp app.example.com
toggling app.example.com → disabled

╭──────────────────────╮
│ cloudflared updated  │
╰──────────────────────╯
```

## forge

Set up cloudflared tunnel routing for all of an app's endpoints at once.

```bash
norn forge <app>
```

Configures cloudflared ingress rules based on the app's `endpoints` in its infraspec. Use `norn endpoints toggle` for per-hostname control.

## teardown {#teardown}

Remove cloudflared tunnel routing for all of an app's endpoints at once.

```bash
norn teardown <app>
```

Removes the app's entries from the cloudflared ingress configuration. Use `norn endpoints toggle` for per-hostname control.

## version

Display CLI version and API endpoint.

```bash
norn version
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--api` | Override the Norn API URL (default: `NORN_URL` or `http://localhost:8800`) |
