# Next M2 slice: named consumers and runtime delivery

Start only after the current batch is terminal and independently accepted.
Full M0–M9 remains the goal. Do not stop at another standalone resolver.

## Contract disposition

Approved for local implementation: separately versioned norn.database/v1alpha1
catalog and schemaVersion norn.app/v2 InfraSpec, strict unknown-field handling,
explicit logical-resource binding, independent NORN_DATABASE_PROFILE, and
rejection of mixed named/legacy declarations. Keep Fleet v1 unchanged. Catalog
examples must remain executable and validate against actual decoder behavior.

Runtime delivery still needs precise semantics: a URL-file path must not be
presented as a connection URL. Separate file-path variables (for example
DATABASE_URL_FILE) from actual connection-valued variables (DATABASE_URL).
Implement private allocation-side templating that can deliver a real connection
value to conventional Node/PG clients without embedding credentials in submitted
job specs, operation evidence or inspection. Verify the installed Nomad API
contract, ACL/variable scope, template change/restart behavior and all web,
worker, cron and function translation paths. An explicit file-consumer mode may
coexist, but cannot substitute for ordinary client compatibility. Migration
commands need the same selected target and supported client delivery; a libpq
service URL alone does not cover Node migrations.

## Delivery

- Connect named resource resolution to acceptance, execution and health, retaining
  exact target integers, retry semantics and endpoint-generation fencing.
- Carry the selected target to runtime, migrations, snapshot/restore and probes;
  reject app/process/secret variable conflicts before side effects.
- Make inventory, export and readiness use the same target-aware snapshot access
  layer as restore. Do not leave misleading database-name-only backup claims
  when a profile is enabled. Verify authorization and path isolation.
- Add bound app.deploy integration through real acceptance/worker boundaries,
  not just direct data-operation tests. Prove same-name two-server consistency.
- Add supported authenticated catalog activation and redacted inspection with
  durable idempotency and expected-revision checks. Reuse existing admission and
  mutation evidence; do not add an unaudited configuration write endpoint.
- Keep PostgreSQL and MySQL capability reports honest. Implement MySQL only with
  its distinct adapter and actual scoped engine test evidence; no installs or
  privileged runtime without further authority. Other available M2 work proceeds
  while that qualification remains outstanding.

Preserve the initial Mini compatibility lane without silently rerouting it.
Explicitly report legacy inherited migration environment as unqualified; do not
claim named-target safety covers it. Cross-target restore and namespace adoption
require explicit source/target identity, never a filename or blanket boolean.

Tests must include secret canaries, real runtime-client connection behavior,
generation changes after acceptance, identical retries, same-name snapshots,
authorization failures, and crash boundaries. Qualify TLS against a scoped local
server before advertising verified TLS connection support.

No commits, pushes, deploys, SSH, cloud provisioning, external-repository edits,
package installation or privileged containers. Continue using Claude CLI for
implementation and root for independent review. Retention remains the next full
vertical after database-consumer integration, not optional follow-up work.
