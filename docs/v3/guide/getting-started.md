# Start with Norn 3.0.0

Norn connects your application definition to builds, deployments, readiness,
and recovery. Start with [why Norn](why-norn.md), then choose a release strategy.

## Choose a release strategy

| Model | Control state and operation | Current boundary |
| --- | --- | --- |
| Fleet | Multiple nodes use etcd control state, separate management, and protected infrastructure execution. DigitalOcean is the current adapter. | A three-control online upgrade is qualified for the recorded source pair and topology. Other provider adapters are future work. |
| Self-hosted | A single host uses PostgreSQL control state and a local host lifecycle. | It is not a Fleet member. Fleet pool resizing and control rotation do not apply to it. |

Read the [architecture](../architecture/overview.md) before choosing resource
sizes or credentials. Use the [capability reference](../reference/capabilities.md)
to distinguish implemented behavior from live-qualified behavior.

## Prepare the local tools

| Tool | Baseline | Use |
| --- | --- | --- |
| Go | 1.26.10 | API, CLI, and host tools |
| Node.js and pnpm | Node 24, pnpm 10+ | Dashboard and documentation |
| PostgreSQL | 16+ | Self-hosted control state |
| Docker | 24+ | Application container builds |
| Nomad and Consul | 1.9+ and 1.20+ | Scheduling, discovery, and health |
| SOPS and age | 3.9+ and 1.2+ | Encrypted application secrets |

The Fleet release strategy additionally needs its pinned etcd/runtime versions,
provider adapter, trusted management/runner, state backend, and credentials.
Use the infrastructure runbook for those environment-specific pins.

## Work with the current source

The active API, CLI, dashboard, and platform scripts still live under `v2/` in
`antiartificial/norn`. V3 is the product release, not a source-directory rename.
The separate private `antiartificial/norn-fleet` repository owns infrastructure
configuration, provider adapters, and protected runners.

For a local build, use Go 1.26.10, Node.js 24, and the repository's pnpm tooling.
Run these commands from the Norn checkout:

```sh
make -C v2 doctor
make -C v2 build
pnpm --dir v2/ui install --frozen-lockfile
make -C v2 ui
```

The Go binaries are written to `v2/bin/`. To run a local development environment,
use the same shell so the API inherits the database setting:

```sh
cd v2
createdb norn_v2
export NORN_DATABASE_URL='postgresql:///norn_v2?sslmode=disable'
export NORN_APPS_DIR="$HOME/projects"
make up
pnpm --dir ui dev
```

PostgreSQL must allow your local user to create and own the database. The API
applies its schema migrations at startup. `make up` starts local Consul, Nomad,
and the API; the last command starts the dashboard in the foreground. The API
listens on `127.0.0.1:8800` and the dashboard on `127.0.0.1:5173`. Use `make down`
from `v2/` to stop the background runtime.

This is a local development setup with development defaults. Fleet bootstrap
and hardened self-hosted installation require their own trusted credentials,
runtime configuration, and release procedure.

## Define and inspect your first app

Place a reviewed [application definition](applications.md) beneath the API's
configured `NORN_APPS_DIR`, set to `$HOME/projects` in this example. Keep `deploy: false` while preparing it. Install
`v2/bin/norn` on your PATH or use its full path, configure its target endpoint,
and inspect the application. For this local environment:

```sh
export PATH="$(pwd)/bin:$PATH" # run from v2/ in a second terminal
export NORN_URL=http://127.0.0.1:8800
norn status
norn app hello-world
norn validate --file /path/to/hello-world/infraspec.yaml
norn preflight hello-world <pushed-commit-sha>
```

Follow the [operator workflow](../operations/workflow.md) to enable deployment,
inspect its result, and verify the application's endpoint. Preflight is not a
serving deployment.

## Operate a Fleet

1. Identify the exact source, signed artifact, Fleet state, hosts, and owner.
2. Check the operation's [qualification status](../reference/capabilities.md).
3. Follow the [online-upgrade procedure](../operations/online-upgrades.md) for
   an existing compatible Fleet.
4. Retain terminal evidence outside the Fleet before any retirement.

The staging pilot no longer exists. Fresh creation is a separate qualification
lane; the successful upgrade does not establish a new unattended bootstrap.
Management and recreation inputs remain retained. Do not re-enable the disabled
ordinary Fleet apply workflow without refreshing historical state and reviewing
its plan.
