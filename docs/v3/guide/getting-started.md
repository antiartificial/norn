# Start with Norn 3.0.0

Norn V3 adds a tested Fleet platform-upgrade path to the existing application
control plane. Start by choosing the environment you intend to operate.

## Choose the deployment model

| Model | Control state and operation | Current boundary |
| --- | --- | --- |
| Fleet | The tested DigitalOcean topology uses three control nodes with etcd, one active API, separate ingress/application nodes, and external management authority. | The live online platform upgrade is qualified for the recorded source pair and topology. The pilot is now retired. |
| Independent self-hosted node | The Mini runs independently with its own PostgreSQL control state and direct host lifecycle. | It is not a Fleet member. Fleet pool resizing and control rotation do not apply to it. |

Read the [architecture](../architecture/overview.md) before choosing resource
sizes or credentials. Use the [capability reference](../reference/capabilities.md)
to distinguish implemented behavior from live-qualified behavior.

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

The Go binaries are written to `v2/bin/`. Building them does not create a Fleet
or publish a signed release. The existing [local development guide](../../v2/guide/getting-started.md)
contains the PostgreSQL and runtime setup used by the local implementation.
It is not a Fleet bootstrap procedure.

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
