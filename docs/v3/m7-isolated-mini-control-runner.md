# M7 isolated Mini control runner

The M7 mobility fixture needs named database routing, while the Mini's primary
API currently manages legacy apps through ambient PostgreSQL configuration.
Activating a global database catalog on that primary API could redirect or
block those apps. This operator lane starts a second, loopback-only API from
the exact signed platform release. It shares Mini's Nomad and Consul endpoints,
but has a distinct PostgreSQL control database, app catalog, catalog profile,
secret directory, token, and audit signing key.

It is not an HA claim and it is not the production Mini API. The fixture is the
only app in its reviewed catalog, and `NORN_APP_CATALOG_READ_ONLY=true` blocks
API changes to that catalog. That does not constrain every control API route:
the dedicated raw M7 token remains privileged for this control process. The
current boundary is a loopback listener, owner-only roots and encrypted inputs,
separate control/database identities, and a short-lived operator session. Keep
the fixture deploy-disabled until its image, database binding, volume, and
write fence are separately reviewed.

## Inputs prepared before any runner command

Create each path owner-only before use. The runner refuses symlinks and modes
other than `0700` for directories and `0600` for input files.

* A new `norn_m7_*` PostgreSQL control database. It must not be the Mini's
  primary control database. The runner normalizes endpoint-plus-database before
  accepting it, rejects an identity matching the primary API's DSN, and records
  a read-only SQL identity receipt before the first schema migration.
* An isolated catalog directory containing only `v3-mobility-fixture` and its
  reviewed InfraSpec. The fixture declares `mobility-primary`, a global-logical
  name that cannot collide with existing `primary` bindings.
* An isolated database secret directory and a catalog activation in the new
  control database for profile `mini-m7`, with a `mobility-primary` binding.
* An encrypted M7 JSON environment containing only these dedicated values:
  `NORN_M7_DATABASE_URL`, `NORN_M7_API_TOKEN`, `NORN_M7_AUDIT_SIGNING_KEY`,
  `NORN_M7_NOMAD_ADDR`, and `NORN_M7_CONSUL_ADDR`.
* The existing encrypted primary API environment only for an in-process
  equality check. The runner rejects matching control DSNs, API tokens, or
  audit keys and never writes either primary value to its generated runtime
  environment.
* The full-SHA signed release directory and its pinned public verification key.

The encrypted files are decrypted only inside the runner process. It writes a
temporary mode-`0600` runtime environment under the M7 private root, removes
it after the controlled API exits, and starts the API with `env -i`; inherited
primary API credentials cannot reach the M7 process.

## Ordered operation

Set the explicit opt-in and private paths, then use the exact release:

```bash
export NORN_M7_ISOLATED_CONTROL_RUNNER=1
export NORN_M7_CONTROL_ROOT=/private/m7-control
export NORN_M7_CONTROL_APPS_DIR=/private/m7-apps
export NORN_M7_CONTROL_SECRET_DIR=/private/m7-db-secrets
export NORN_M7_CONTROL_ENV_FILE=/private/m7-control.enc.json
export NORN_M7_PRIMARY_ENV_FILE=/private/primary-api.enc.json
export NORN_M7_CONTROL_PROFILE=mini-m7
export NORN_M7_CONTROL_PORT=18810
export NORN_SOPS_BIN=/opt/homebrew/bin/sops
export NORN_M7_CANDIDATE_RELEASE=/private/releases/<40-character-sha>
export NORN_M7_RELEASE_PUBLIC_KEY=/private/norn-release.pub
```

1. Run `v2/scripts/mini-m7-isolated-control-runner bootstrap`. It runs only
   schema migration against the new M7 control database and exits. It does not
   start workers.
2. Run `v2/scripts/mini-m7-isolated-control-runner catalog-bootstrap`. It
   starts the isolated API with the database profile deliberately unset, so it
   can accept the first durable catalog activation without pretending that a
   catalog already exists. Submit and wait for exactly one reviewed activation
   for profile `mini-m7` and its `mobility-primary` binding, then stop this
   process. Named fixture deployments remain refused in this bootstrap mode.
3. Run `v2/scripts/mini-m7-isolated-control-runner preflight`. This starts a
   passive, loopback-only schema check, requires that its own PID is the sole
   listener, verifies workers/recovery/watchers are disabled through
   `/api/schema`, then stops the process.
4. Run `v2/scripts/mini-m7-isolated-control-runner run` in the controlled
   maintenance shell. This is the only active mode. It enables operation
   execution for the isolated control database so a reviewed fixture deploy or
   one-off migration can occur. Keep it running only through the bounded M7
   source exercise and preserve returned operation, deployment, allocation,
   migration, and fixture-state receipts.
5. Stop the active process after the source exercise, re-run passive
   preflight, and retain the redacted catalog/version/schema evidence. Do not
   infer that this runner changes or validates the primary Mini API.

The runner intentionally does not activate the catalog, create credentials,
create a database, deploy the fixture, or provision a host volume. Those are
separate reviewable changes. Before fixture scheduling, provision the declared
host-volume path with UID/GID `65532` and mode `0700`, then use the
fixture-specific volume name `m7-mobility-files` and exact signed image digest.
