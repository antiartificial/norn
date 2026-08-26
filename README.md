# Norn

Norn is a local-first control plane for running applications on Nomad and
Consul. It gives operators one API, CLI, web dashboard, and native-client
contract for application delivery, regional ingress, durable maintenance,
host recovery, and fleet planning.

Norn v2 is the active implementation. The Kubernetes/minikube-based v1 tree is
frozen and retained only for historical compatibility; its documentation lives
under [`docs/v1`](docs/v1/guide/getting-started.md).

[Read the Norn v2 documentation](https://antiartificial.github.io/norn/)

## What Norn manages

- Multi-process apps described by `infraspec.yaml`: HTTP services, workers,
  cron jobs, and functions.
- Nomad allocations and Consul service registration across one or more regions.
- Consul-backed Traefik ingress, health-gated traffic weights, canaries, and
  independently tracked regional rollback.
- Durable deploy, rollback, snapshot, retention, restore, schema-migration,
  platform-upgrade, and host-assurance operations stored in PostgreSQL.
- SOPS-encrypted app secrets, dependency provisioning, Cloudflare endpoints,
  service discovery, and Prometheus/Grafana integration.
- Beacon events, incidents, notifications, recovery evidence, and signed
  production mutation receipts.
- Private GitOps fleet planning without putting cloud-provider credentials in
  the Norn control plane.

## Architecture

```text
Web UI / macOS / mobile / CLI
              |
        Norn API + workers
              |
     PostgreSQL durable state
              |
      Nomad + Consul + Traefik
              |
       Application allocations

Private norn-fleet repository
              |
 GitHub review + OpenTofu runner
              |
      Cloud nodes enroll in Norn
```

`norn-fleet` owns desired cloud infrastructure such as providers, regions, VM
sizes, networking, and node-pool capacity. Norn validates that configuration,
creates durable capacity plans, links reviewed GitHub workflows, observes node
enrollment and readiness, and drains replaced nodes. Provider and Terraform
state credentials remain in the protected infrastructure runner.

## Prerequisites

| Tool | Supported baseline | Purpose |
|---|---:|---|
| Go | 1.26.6 | API, CLI, and host agent |
| Node.js | 24 LTS | Dashboard and documentation |
| pnpm | 10+ | Front-end dependencies |
| PostgreSQL | 16+ | Durable control-plane state |
| Docker | 24+ | Local development builds |
| Apple Container | optional | Native macOS 26 development connector |
| Nomad | 1.9+ | Scheduling |
| Consul | 1.20+ | Discovery and health |
| SOPS + age | 3.9+ / 1.2+ | Secret encryption |

Production deployments also require the TLS, ACL, external-database,
immutable-artifact, audit, and recovery-drill evidence described in
[Production Readiness](docs/v2/operations/production-readiness.md).

## Build and run locally

```bash
git clone git@github.com:antiartificial/norn.git
cd norn/v2

make doctor
make build      # norn-api, norn, and norn-host-agent
pnpm --dir ui install
make ui         # production dashboard assets
```

For interactive UI development, run `pnpm dev` from `v2/ui`; the API listens on
`127.0.0.1:8800` by default and Vite listens on `127.0.0.1:5173`.

Create the control-plane database before starting the API. Norn applies its own
schema migrations on startup:

```bash
createdb norn_v2
export NORN_DATABASE_URL='postgresql:///norn_v2?sslmode=disable'
make up         # local Consul, Nomad, and API
```

The local-socket URL connects as the current operating-system user, which owns
the database created above; the PostgreSQL server still controls its local
authentication policy. The code's `norn:norn` TCP default is only suitable when
that PostgreSQL login and ownership have been created explicitly. Start the
dashboard separately with `pnpm --dir ui dev`; the v2 `make dev` target runs
only the API in the foreground.

See [Getting Started](docs/v2/guide/getting-started.md) for complete setup and
configuration guidance.

## Define an app

Norn discovers `infraspec.yaml` files beneath `NORN_APPS_DIR`. New applications
default to `deploy: false`; review and validate the generated spec before
explicitly enabling deployments.

```yaml
name: hello-world
deploy: false

repo:
  url: git@github.com:you/hello-world.git
  branch: main
  autoDeploy: false

build:
  dockerfile: Dockerfile
  test: go test ./...

primaryRegion: primary
regions:
  primary:
    nomadRegion: global
    datacenters: [dc1]
    trafficWeight: 100

processes:
  web:
    command: ./hello-world
    port: 8080
    health:
      path: /health
    scaling:
      min: 2
    resources:
      cpu: 200
      memory: 256
    metrics:
      enabled: true
      path: /metrics

infrastructure:
  postgres:
    database: hello_world

secrets:
  - DATABASE_URL

endpoints:
  - url: https://hello.example.com
```

Normal service processes target all declared regions by default. Cron and other
singleton processes remain in `primaryRegion` unless their process placement is
explicitly narrowed. Endpoint-backed services can run multiple allocations
behind the regional Traefik origin; traffic is promoted only after application
readiness succeeds.

Validate and rehearse the disabled draft:

```bash
norn validate --file /path/to/infraspec.yaml
norn preflight hello-world <commit-sha>
```

Then enable the reviewed spec from the Apps dashboard or the versioned control
API. The current CLI has no deployment-gate subcommand:

```bash
curl --fail-with-body -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true}' \
  http://127.0.0.1:8800/api/v1/apps/hello-world/deployment

norn deploy hello-world <commit-sha>
```

Add `Authorization: Bearer $NORN_API_TOKEN` to the API request when explicit
authentication is enabled. Disabled drafts remain visible and preflightable,
but runtime deploy and recovery paths ignore them until this gate is enabled.

Use an immutable, pushed commit SHA for operational changes. `HEAD` remains a
convenient development alias, but it is not a reproducible release reference.

## Daily operator workflow

```bash
norn status
norn app hello-world
norn services manifest
norn operations --active
norn events active
norn events reconcile --dry-run
norn deploy steps <deployment-id>
norn logs hello-world
```

App changes run through the durable operations queue. Accepted or queued means
the worker still has work to perform; follow the operation or saga to a terminal
state and then probe the user-facing readiness path.

### Durable application recovery

```bash
norn snapshots hello-world
norn snapshots hello-world restore <unique-compact-utc-timestamp> --yes --pre-restore
norn rollback hello-world
```

The versioned `/api/v1` control API and web/native clients can also queue manual
snapshots, reviewed pruning, preferred exact-inventory-filename restores with
mandatory safety snapshots,
standalone migrations, and rollback. Idempotency keys and PostgreSQL receipts
let clients reconnect after an ambiguous response without duplicating the
mutation. The current CLI restore command uses the legacy synchronous route and
accepts a compact UTC timestamp only when it matches one inventory entry. Local
snapshot files are node-local; HA production systems should use managed database
PITR or off-host object storage.

### Platform and host recovery

Resolve an exact pushed commit before upgrading Norn itself:

```bash
release_sha=$(git rev-parse origin/main)
norn platform queue-preflight "$release_sha"
norn platform queue-upgrade "$release_sha"
norn platform releases
norn smoke platform
```

The independent host agent executes queued platform maintenance and survives
the API restart. Platform commands add common Homebrew tool directories when
invoked through a thin SSH shell, so the release build can still find Node,
pnpm, Go, SOPS, and related tools without a manually amended remote `PATH`.

On a persistent macOS host, install and inspect the recovery lane:

```bash
norn host install --repo /path/to/norn
norn host recover
norn host assure
norn host status
norn host doctor
```

Recovery starts Docker, Consul, Nomad, and Norn in dependency order. Assurance
then checks explicitly required applications, minimum allocation counts,
routes, and real HTTP entrypoints; it repairs only resources allowed by the
installed policy and emits Beacon failure/recovery evidence.

## Fleet planning

Point Norn at a read-only checkout of the private fleet repository:

```bash
export NORN_FLEET_CONFIG=/srv/norn-fleet/environments/production/nyc3/cluster.yaml

norn fleet validate "$NORN_FLEET_CONFIG"
norn fleet pools
norn fleet plan app --desired 4 --reason 'launch headroom'
norn fleet github pr <plan-id>
# Review and merge the protected pull request and plan workflow.
norn fleet github apply <plan-id>
```

Capacity plans are durable, signed in production, and bound to the fleet source
digest. Retrying GitHub actions recovers the deterministic pull request or
existing workflow run rather than creating duplicate infrastructure changes.
See [Fleet GitOps](docs/v2/infrastructure/fleet.md) for setup, contraction, and
reconciliation semantics.

## Documentation map

- [Why Norn](docs/v2/guide/why-norn.md)
- [InfraSpec reference](docs/v2/guide/infraspec-reference.md)
- [Architecture overview](docs/v2/architecture/overview.md)
- [Native control protocol](docs/v2/architecture/control-protocol.md)
- [Deploy pipeline](docs/v2/architecture/deploy-pipeline.md)
- [Durable operations](docs/v2/operations/operations.md)
- [Snapshots and schema changes](docs/v2/operations/snapshots.md)
- [Host recovery and assurance](docs/v2/operations/host-recovery.md)
- [Linux HA acceptance lab](docs/v2/operations/ha-lab.md)
- [CLI reference](docs/v2/cli/commands.md)
- [Current release recap](docs/v2/guide/release-recap.md)

## v1 archive

The root-level `api/`, `cli/`, `ui/`, `worker/`, and Kubernetes-oriented
Makefile belong to frozen v1. They remain in the repository to preserve its
tagged history, but new development, deployment, and documentation should use
`v2/` and `docs/v2/`. Do not use the v1 Kubernetes instructions to operate a v2
Nomad/Consul host.
