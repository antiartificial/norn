---
title: Isolated workload qualification
description: Repeatable local load, recovery, and network-degradation evidence for Norn workload connectors.
---

# Isolated workload qualification

`v2/scripts/qualify-workload` produces a portable evidence bundle for an
**explicitly named, disposable, loopback-only** test workload. It is a
qualification tool, not a production load generator and not a Norn deployment
command. It neither discovers applications nor selects allocations, hosts, or
cloud resources.

Use it after deploying a throwaway workload through the connector being tested.
For Apple Container, that means a test Norn API on an alternate local port with
a separate temporary database and apps directory. Do not point it at the Mini,
a tailnet address, a public endpoint, DigitalOcean, or a production API.

## What it records

- Host identity, processor count, memory where the operating system exposes it,
  and Linux load average.
- Optional Norn `/api/v1/host/metrics` and `/api/v1/host/status` evidence from
  an explicitly supplied local API. `NORN_API_TOKEN` (or `NORN_TOKEN`) is used
  in-process when set; it is never written into the report.
- Optional read-only connector/runtime output, such as Apple Container's
  `container stats --format json --no-stream <isolated-name>`.
- HTTP request count, successes/errors, status-code distribution, throughput,
  and p50/p95/p99/max latency.
- Opt-in workload stop/start and opt-in restart/resume evidence for a
  disposable Norn API.
- Opt-in Toxiproxy latency-injection evidence.

The JSON report schema is `norn.workload-qualification/v1`, so results can be
attached to a release receipt or compared in CI without scraping terminal
output.

## Baseline

Start with a local readiness endpoint. The harness accepts only `localhost` or
a loopback IP; this containment is intentional.

```bash
v2/scripts/qualify-workload \
  --endpoint http://127.0.0.1:18080/health \
  --isolation-id norn-qual-apple-smoke \
  --requests 500 --concurrency 25 --timeout 3 \
  --norn-api http://127.0.0.1:18800 \
  --runtime-command 'container stats --format json --no-stream norn-qual-apple-smoke-web' \
  --output evidence/apple-baseline.json \
  --fail-on-errors
```

The workload and its container must be created separately. The explicit
`norn-qual-` prefix makes it clear that it is safe to destroy. A normal Norn
app or allocation name is not accepted as an isolation identifier.

## Controlled recovery

Recovery is intentionally verbose. Both mutation commands must contain the
same isolation id, and the caller must pass `--allow-workload-disruption`.
The harness waits for the readiness URL after the recovery command and records
each step’s exit status in the report.

```bash
v2/scripts/qualify-workload \
  --endpoint http://127.0.0.1:18080/health \
  --isolation-id norn-qual-apple-smoke \
  --allow-workload-disruption \
  --kill-command 'container kill norn-qual-apple-smoke-web' \
  --recover-command 'container start norn-qual-apple-smoke-web' \
  --ready-url http://127.0.0.1:18080/health
```

To test Norn API recovery, launch a disposable API with a temporary database,
separate app directory, and a different local port. Then opt in with all of
the following flags. This is not permitted for the live API:

```bash
--allow-api-restart \
--api-restart-command '<command that restarts only the disposable API>' \
--api-ready-url http://127.0.0.1:18800/api/health \
--isolated-api-confirmation norn-qual-apple-smoke
```

The confirmation is deliberately redundant: it prevents an accidental API
restart from being enabled by merely copying a command flag.

## Network degradation with Toxiproxy

Run Toxiproxy directly on the host and pass its local API, the proxy listener
URL, and a local upstream. The harness creates a proxy named
`norn-qualification-<isolation-id>`, injects fixed downstream latency, runs
the network load phase, and deletes that proxy in its cleanup trap. Existing
proxies are never selected or deleted.

```bash
v2/scripts/qualify-workload \
  --endpoint http://127.0.0.1:18080/health \
  --isolation-id norn-qual-apple-smoke \
  --toxiproxy-api http://127.0.0.1:8474 \
  --toxiproxy-listen-url http://127.0.0.1:18081/health \
  --toxiproxy-upstream 127.0.0.1:18080 \
  --network-latency-ms 250 --network-requests 100
```

Keep Toxiproxy on the host for this harness. Container-to-host gateway names
and addresses differ between Apple Container, Docker, and Linux; accepting
them here would weaken the loopback-only containment guarantee. A containerized
proxy can be qualified in its own dedicated network fixture, but is not an
input to this local safety harness.

## Interpreting results

Compare baseline and network p95/p99 values, not a single fastest response.
For recovery, require the relevant operation record to have `exitCode: 0` and
`ready: true`. For an API restart, separately inspect Norn’s durable operation
receipt: this script proves the test API returned, not that an arbitrary
application mutation is replay-safe.

Suggested staging gates are application-specific, but a useful starting point
is: zero baseline errors, an agreed p95/p99 budget under representative load,
successful readiness after an isolated process restart, and a documented
failure/recovery result under injected latency. Repeat in a Linux Nomad/Consul
lab before treating Apple Container development results as production evidence.
