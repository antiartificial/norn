# Operate an application

Use this workflow against the Norn environment you intend to change. Configure
the CLI endpoint and credential for that environment first. Local development
and Fleet staging or production must not share an assumed default target.

## Discover the current state

```sh
norn status
norn app hello-world
norn services manifest
norn operations --active
norn events active
```

Review the service endpoint and readiness, deployed source, allocation state,
active operations, missing inputs, and recent failure events. Start with the
application's state rather than assuming a healthy API means a healthy workload.

## Validate and rehearse a candidate

```sh
norn validate --file /path/to/hello-world/infraspec.yaml
norn secrets status hello-world
norn preflight hello-world <pushed-commit-sha>
```

Preflight prepares the source and checks the build, tests, and required inputs
without promoting the serving application. Follow its result to completion.
A disabled draft can be inspected and preflighted before its deployment gate
is enabled.

## Deploy and verify service

Enable the reviewed application's deployment gate in the dashboard or control
API, then deploy the immutable candidate:

```sh
norn deploy hello-world <pushed-commit-sha>
norn deploy steps <deployment-id>
norn logs hello-world
```

A queued response means work was accepted. Inspect the terminal operation and
stage evidence, then request the application's actual readiness endpoint.
Confirm the expected version as well as an HTTP success response.

Health-gated routing, canary behavior, snapshots, and migrations depend on the
app's specification and the environment's supported capabilities. Do not infer
a live-qualified workload release protocol from the platform-upgrade result.

## Drain before maintenance

Inspect `norn operations --active` before maintenance. The maintenance controller
must apply its own drain and storage/compatibility checks. Do not bypass a held
operation simply because its worker is no longer running.

An application drain, application replica change, Fleet pool resize, and Norn
platform upgrade are distinct operations. Follow [Fleet operations](fleet.md)
for infrastructure work and [online upgrades](online-upgrades.md) for platform
control rotation.

## Recover a failed change

Inspect the original operation, deployment stages, logs, and current service
before selecting a recovery action. A binary rollback does not reverse a schema
migration or restore application data.

```sh
norn snapshots hello-world
norn rollback hello-world
norn events reconcile --dry-run
```

Snapshot restore requires an explicit inventory selection and a safety snapshot.
Use the environment's supported restore interface. The legacy CLI restore path
and versioned API have different request contracts. See [recovery](recovery.md)
and [operation semantics](operations.md) before retrying an interrupted effect.
