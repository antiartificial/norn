# Next Steps

This page tracks the current development direction for Norn v2. It is intentionally practical: each item should either make the control plane a more reliable source of truth, reduce deployment ambiguity, or improve day-to-day operations for apps hosted under Norn.

## Current Baseline

Norn v2 is the active development path. It uses Nomad for scheduling, Consul for service health, local Docker builds for images, SOPS/age for secrets, and cloudflared for external routing.

The current working feature set includes:

- App discovery from `NORN_APPS_DIR` via `infraspec.yaml`
- Multi-process app specs for web, worker, cron, and function processes
- Deploy pipeline with clone, build, test, snapshot, migrate, submit, health, forge, and cleanup steps
- Nomad service jobs, periodic jobs, and batch function jobs
- Consul-backed health and service registration
- Cloudflared endpoint forge, teardown, and endpoint toggles
- CLI workflows for status, app detail, deploy, logs, health, stats, secrets, snapshots, cron, invoke, saga, validate, forge, and endpoints
- Saga event log and WebSocket deploy progress
- Basic local snapshot listing and restore
- Port conflict detection before Nomad submit

## Immediate Norn Items

### Service Manifest

The service manifest is the next discovery surface for agents, dashboards, and external tooling. It should become the compact answer to "what is hosted, where is it, and is it healthy?"

Planned work:

- Finish `/api/services/manifest` and add tests for the manifest schema. The first schema test now covers ContextDB-style web plus review-worker specs.
- Distinguish app-level endpoints from process-level reachability. The manifest now only attaches app endpoints to service processes.
- Mark service processes, worker processes, cron jobs, and functions with explicit process type metadata.
- Avoid giving a worker or cron process a public endpoint inherited from the app unless that endpoint is actually routable to it.
- Include internal Consul/Nomad addresses separately from public or cloudflared endpoints.
- Add CLI support, for example `norn services` and `norn services manifest`. The first CLI slice is available.
- Document the manifest contract for agents and MCP/tool discovery.

### Deploy Provenance

Norn can deploy from a local working tree fallback, but local dirty builds currently look like the last git commit. That can make the runtime look cleaner than it really is.

Planned work:

- Detect dirty local trees during deploy.
- Record dirty state and changed-file summary in the deployment record and saga. The first slice records a `source.provenance` saga event for clone, local-copy, and local-fallback sources.
- Use an image tag or metadata suffix that makes local dirty builds obvious. Local dirty builds now append `-dirty` to the image tag's commit segment.
- Show dirty/deployed provenance in `norn app`, `norn status`, and the UI. `norn app` now surfaces the latest deployment status, image tag, resolved commit, and saga id; `norn status` shows latest image and commit columns.
- Preserve the exact commit SHA and build timestamp for every deployed image.

### Networking Truth

Norn dev mode binds Nomad container ports to `127.0.0.1`, while some app endpoints may refer to Tailscale or public hostnames. The docs and manifest need to make the difference visible.

Planned work:

- Separate `localhost`, Tailscale, cloudflared, and internal Consul addresses in app status and service manifest output.
- Warn when an endpoint points at an address that is not actually reachable in the current Nomad mode.
- Add an explicit networking mode indicator to `norn health`, `norn app`, and the service manifest.
- Document when to use `host.docker.internal`, `127.0.0.1`, Tailscale IPs, and cloudflared hostnames.

### Secrets Hygiene

Several app specs still carry sensitive DSNs or credentials in plain `env` blocks. Norn should make the secure path easier than the unsafe path.

Current state:

- `norn validate` warns when top-level or process-level `env` blocks contain secret-like DSNs, passwords, tokens, API keys, client secrets, or provider keys.
- App and process `env` values are parsed for validation but omitted from API JSON responses.

Planned work:

- Move sensitive DSNs and API keys into `secrets.enc.yaml`.
- Improve `norn secrets` UX for set, delete, list, and deploy-time resolution.
- Show secret names, never values, in UI and CLI app detail views.

### Snapshot Semantics

Snapshots exist, but the current implementation is closer to local operational support than production-grade backup management.

Planned work:

- Clarify local snapshot storage versus S3-backed storage.
- Add retention policy and pruning.
- Add restore safety rails, including optional pre-restore snapshot and confirmation.
- Emit restore and snapshot receipts into the saga log.
- Show snapshot provenance in app detail and deploy history.

### Observability And Access

The next operational layer is to show who is reaching Norn-managed services and make temporary access explicit.

Planned work:

- Add Norn API request logging middleware with method, path, status, user agent, and client IP.
- Preserve Cloudflare client IP metadata when available.
- Add a traffic/access page in the UI.
- Explore a shared gateway for app-level request logging.
- Add temporary access grants, such as expiring JWT links or expiring IP allowlist entries.

## ContextDB Items

ContextDB is now a useful proving ground for Norn because it has both a web process and a background review worker. The big ContextDB items split into deployment hygiene, worker operations, and product-level context quality.

### Deployment Hygiene

Current state:

- ContextDB is managed by Norn as separate `web` and `review-worker` processes.
- The service manifest reports ContextDB web as a routable service and the review worker as a non-public worker.
- ContextDB ships a Norn smoke script that checks web health, worker health, write/retrieve behavior, review queue setup, and an in-allocation worker dry run.
- ContextDB validates cleanly under Norn's plaintext-secret checks: `CONTEXTDB_DSN` is present as a Norn secret and is not duplicated into plain `env`.

Planned work:

- Decide whether ContextDB should expose only local Norn endpoints, a Tailscale endpoint, a cloudflared endpoint, or some combination.
- Promote the smoke script into a first-class Norn runbook or `norn smoke contextdb` command.

### Worker Operations

Current state:

- The deployed Hermes policy runs the review worker in dry-run mode with the keyless `rules` evaluator, `stale-only` policy preset, and conservative allowed actions.
- One-shot worker dry runs work through `norn exec contextdb --process review-worker -- /contextdb worker review ... --report`.
- ContextDB records durable review worker summaries through its own API surface.

Planned work:

- Decide when a namespace is allowed to move from dry-run to conservative execution.
- Surface ContextDB worker run summaries as Norn metrics, Norn saga events, or CLI/UI output.
- Add worker logs and dry-run decision reports to the UI or CLI.

### Evaluators

Current state:

- `rules` is the no-key default evaluator.
- `webhook`, `openai`, `anthropic`, `xai`, and provider-backed `hybrid` evaluators are implemented and explicitly configured.
- `--smoke-evaluator` checks evaluator configuration without opening the database or mutating claims.
- Worker reports include the effective evaluator for each namespace.

Planned work:

- Store provider evaluator keys as Norn secrets when enabling provider-backed policies.
- Add a Norn-level provider evaluator smoke/runbook before allowing provider-backed policies in production.
- Add UI/CLI affordances that clearly distinguish rules, webhook, and provider-backed worker decisions.

### Context Quality

- Add recurring review of low-confidence, stale, contradicted, and source-anomaly claims.
- Add durable receipts for validate, refute, stale, prune, and manual review decisions.
- Add source quarantine and trust repair workflows for repeatedly refuted sources.
- Add dashboards for review queue size, stale claim count, source trust drops, and worker actions.
- Add namespace-specific review policies for Hermes agent memory versus other ContextDB users.

### Agent Integration

Current state:

- Claim execution remains centralized in the ContextDB worker rather than per application.
- Agents can discover ContextDB web and worker health through the Norn service manifest.

Planned work:

- Make Hermes use ContextDB review APIs for queue inspection, claim validation, refutation, and pruning.
- Add a "human review required" lane for decisions the worker will not execute automatically.

## Suggested Order

1. Finish moving app plaintext secrets into `secrets.enc.yaml`, using `norn validate` as the guardrail.
2. Promote the ContextDB smoke script into a first-class Norn smoke/runbook command.
3. Improve networking truth in app detail and manifest output.
4. Surface ContextDB worker run summaries and review metrics in Norn.
5. Wire Hermes to ContextDB review APIs for queue inspection, claim validation, refutation, and pruning.
6. Add provider-backed evaluator secrets and smoke checks only when a namespace is ready to leave the keyless rules path.
7. Build the access/traffic dashboard.
