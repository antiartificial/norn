# Normal Fleet release admission contract — 2026-09-27

Status: local implementation boundary for M4. The normal staging
`app.deploy` producer is opt-in behind `NORN_ETCD_FLEET_RELEASE_HTTP=true` and
requires the separately configured Fleet deploy worker. It has not been
enabled or qualified on protected Fleet hosts.

The normal etcd runtime now has a private
`buildEtcdFleetReleaseAcceptance` function for the first staging deployment.
It requires an artifact verifier callback, an enabled server-owned spec,
matching control-owned target placement, and a bound database target set. Its
unit test covers successful construction and refusal before verification for
missing verifier, disabled spec, wrong placement, wrong repository, and a
failed verifier. The opt-in HTTP route now calls it after authenticated
principal binding and exact replay lookup. The restricted store call
atomically accepts the signed deployment; production admission remains closed.

`handler.BindFleetStagingReleaseCandidate` now provides the narrow CI identity
binding for that route. It requires a managed `release:stage` token scoped to
the app and staging environment, the protected default-branch push lane, and
an exact source SHA. It derives every identity field from the verified token
and carries only attestation evidence fields from the request. Focused tests
reject wrong app, environment, intent, source, unprotected ref, non-managed
credential, and missing scope. The route calls this helper and the configured
artifact verifier before accepting a deployment.

A disposable real-etcd HTTP test accepted a first staging release, returned
the same signed operation after token rotation and Fleet target replacement,
restricted operation status to the originating CI run, and rejected a
different artifact under the same idempotency key without repeating artifact
verification. The test injects a successful artifact verifier and does not
prove registry, signature, scan, Nomad, ingress, or public traffic behavior.
Before enabling the flag, the verifier policy, scoped GitHub OIDC exchange,
worker credentials, supported app shapes, and protected traffic rollback
still require review and rehearsals.

## Entry and trust boundary

Use the existing authenticated release-deployment route shape. A verified CI
principal must have release-stage scope for the app. Direct staging deploys
are allowed only in the staging control environment; production requires a
signed staging qualification and release-promote scope. The existing PG
release handler's source SHA, immutable OCI digest, candidate identity,
repository/workflow binding, and production-consumption checks are the
reference behavior. The etcd path must run a configured artifact verifier
with equivalent registry digest, signature/attestation, and scan checks before
writing any acceptance. A missing verifier is a startup/configuration error,
not a reason to accept candidate metadata alone.

The request body may supply only release evidence. It may not supply an
`OperationAcceptance`, deployment ID, placement, database target, signed
semantics, actor, or operation payload. Those values are derived by the
server after authentication and verification. Preserve the existing
idempotency-key contract and conflict behavior; on replay, resolve the exact
signed acceptance before performing a fresh external verification so a
registry outage does not turn an already accepted request into a second
deployment. A distinct request with the same key is a conflict.

## Server-derived aggregate

1. Load the enabled, checked-out InfraSpec for the route app and validate its
   repository and OCI namespace against the verified candidate. Pin a digest
   of the exact spec used for the worker's later binding check.
2. Read the control-owned Fleet target for `(app, control environment)` and
   derive the sole accepted region, Nomad region, datacenters, and 100%
   placement. Reject a disabled spec or a placement mismatch. The request
   cannot choose or create a target.
3. Read the active database catalog and use
   `fleetdeploy.BindFirstFleetDeploymentDatabases` for the configured profile.
   Carry only named identities and catalog revision in the signed payload;
   reject unsupported migration, snapshot, restore, or ambient legacy PG
   requirements before acceptance.
4. Construct queued operation, deployment, region, audit context, actor and
   canonical fingerprint from verified evidence and server-owned inputs.
   Require exclusive app admission. Do not provide the generic public etcd
   `Accept` method with a deployment aggregate.
5. Accept atomically with the existing etcd app gate, Fleet target revision,
   active catalog pointer revision, signed intent, deployment and region
   records. A changed target or catalog makes the attempt stale; reread and
   require a new acceptance decision rather than silently retargeting it.
6. Wake the opt-in worker only after a durable accepted receipt. Return the
   operation resource and distinguish replay from a new `202 Accepted`.

The private `acceptDeploymentAggregate` is currently package-private and
test-only. Expose a narrow trusted producer interface only after steps 1–4
are implemented and tested together. Do not export raw aggregate acceptance
to HTTP or to a package that can be reached without release verification.

## Qualification sequence

- Unit tests: wrong app/repository/workflow, mutable image tag, missing
  verifier, invalid attestation/scan, unsupported database lifecycle,
  disabled/mismatched placement, and production without qualification all
  write no acceptance.
- Disposable etcd: exact replay, changed request under same key, concurrent
  app gate, target replacement, catalog activation between read and commit,
  and ambiguous commit response. Inspect the durable signed receipt and
  deployment/region rows, not only HTTP status.
- Disposable Nomad and two local ingress nodes: a normal accepted release
  reaches the opt-in worker, reconciles the exact job and route, and exposes
  traffic. Inject a partial publisher failure and verify repair or safe
  withdrawal before using protected hosts.
- Protected Fleet: stage with explicit operator scope, budget and rollback
  boundary; prove a clean deployment and 2→3→2 loaded capacity change. Keep
  M4 open until this evidence is captured.

Production promotion and later database lifecycle work remain separate
qualification gates. The first release slice should enable staging only,
then add production with a signed qualification and consumption fence.
