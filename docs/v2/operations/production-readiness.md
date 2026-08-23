# Production readiness

Norn's production profile is an explicit admission boundary for trusted,
single-tenant workloads. It does not infer production intent from a public
endpoint, an app name, or a large allocation count.

## Inspect before enabling

```bash
norn production check
norn production check --json
```

The command reads `GET /api/v1/production/readiness` and exits non-zero while a
required gate fails. The response uses the
`norn.production-readiness/v1` schema and contains only value-safe booleans,
counts, app names, and remediation. It does not return DSNs, tokens, certificate
material, secret values, or private keys.

Before the profile is enabled, this command is diagnostic and deliberately
exits non-zero because `profile.production` remains blocked. Resolve every other
gate, enable the profile, then rerun the command for the final ready/blocked
result.

The current contract checks:

- production-profile enforcement, explicit API authentication, control
  credentials, and retirement of legacy token signing;
- Nomad and Consul reachability, ACL enforcement, verified TLS, server quorum,
  and Nomad client capacity;
- PostgreSQL transport security, an external failure domain, WAL archiving for
  point-in-time recovery, and at least one streaming standby;
- strict-secret findings and plaintext migration items;
- the latest successful deployment provenance and OCI digest pin for each app;
- snapshot freshness and off-site export policy for declared PostgreSQL apps;
- recent database-restore, artifact-rollback, and node-failover drill receipts;
- Prometheus, Grafana, and cAdvisor service health; and
- durable, principal-aware mutation audit persistence and receipt integrity.

Review of authentication-exempt metadata remains a warning. Production-safe
field minimization is still required before regulated or multi-tenant use.

## Enable the profile

Set:

```bash
NORN_PROFILE=production
```

The profile enables strict-secret behavior, artifact admission, and live
substrate admission. The readiness report must confirm these configuration
prerequisites before critical mutations are allowed:

- `NORN_REQUIRE_EXPLICIT_AUTH=true`;
- a production control credential;
- verified HTTPS Nomad and Consul API addresses, with insecure verification
  overrides rejected;
- a PostgreSQL DSN with `sslmode=verify-full`;
- `NORN_REGISTRY_URL`;
- `NORN_AUDIT_SIGNING_KEY` containing at least 32 bytes, stored through the
  same encrypted runtime environment as other control-plane secrets;
- strict-secret validation;
- an expired legacy token-signing compatibility window.

Production app deploys and preflights gain an `admission` stage after source
resolution. It rejects local fallbacks, local copies, dirty checkouts, missing
registry configuration, missing build configuration, strict-secret errors,
services without health checks, and endpoint-backed processes that request
multiple allocations before Norn has a load-balanced endpoint implementation.
Production requires an externally published `build.image` pinned by OCI digest;
Norn does not hold a publisher private key. Before deploy or rollback it resolves
the digest from the registry, verifies a Cosign signature carrying the exact
`norn.git.sha` source annotation, and rejects configured Trivy vulnerability
severities. The publishing lane should emit maximum-provenance and SBOM
attestations. Development behavior is unchanged when the profile is not enabled.

Production runtime mutations are additionally guarded by a five-second live
substrate admission cache. App, deploy-group, webhook-deploy, platform-release,
and ContextDB rollback requests return the stable
`production_substrate_unready` problem when Nomad/Consul reachability, ACL/TLS,
quorum, client capacity, PostgreSQL TLS/external/PITR/replica posture, or durable
audit is not ready. Host assurance, identity/token operations, event workflow,
and recovery-drill recording stay available so an outage cannot lock out its
own repair path.

Every mutating `/api` request reserves a PostgreSQL audit receipt before its
handler runs. In production, failure to reserve the receipt rejects the request
before side effects. Completion records the principal, device/token IDs,
method, route template, result, timing, and an HMAC integrity digest. Bodies,
secret values, and command arguments are not recorded. A crash or finalization
failure intentionally leaves a visible `started` receipt for investigation.
Bulk access-observation and Cloudflare Logpush ingestion are excluded from the
per-request control audit to avoid turning telemetry batches into unbounded
control receipts. Their aggregate data remains on the dedicated observation
surfaces. Completed audit receipts are pruned on a bounded cadence according to
`NORN_AUDIT_RETENTION_DAYS` (365 by default and never below 90 in production);
pending receipts are never pruned by this mechanism.

Inspect receipts with:

```bash
norn production audit
norn production audit --limit 200
```

An invalid historical receipt is never edited or deleted to make a gate green.
After investigation, an administrator can attach a separately signed incident
with `norn production audit-incident`; readiness accepts only a verified incident
using a bounded reason code and explanation while retaining the original bytes.

For key rotation, move the former current key into
`NORN_AUDIT_PREVIOUS_SIGNING_KEYS`, install a new current key, restart, and
verify recent receipts before eventually retiring the previous key after the
audit retention window. Receipts store only a short derived key ID, never the
key itself.

## Recovery drill receipts

Readiness requires successful `database.restore`, `artifact.rollback`, and
`node.failover` drills within 90 days. Start the receipt before the exercise and
complete it only after validating the isolated result:

```bash
norn production drill start database.restore --target restore-sandbox
# perform the isolated restore and application verification
norn production drill complete <id> --status passed \
  --evidence snapshot=object-version-id --evidence rto=4m12s

norn production drills
```

Evidence is deliberately bounded metadata, not an upload channel: at most 20
key/value pairs, with 64-character keys and 1 KiB values. Put full logs and
screenshots in durable object storage and record only a receipt ID, object
version, checksum, RTO, or RPO here. The mutation audit records who started and
completed each receipt.

PostgreSQL readiness inspects the live server rather than trusting an
environment declaration. `archive_mode` plus an `archive_command` or
`archive_library` and replica/logical WAL level are required for PITR; at least
one `pg_stat_replication` standby must be streaming. A loopback or local-socket
DSN fails the external-failure-domain check.

The repeatable DigitalOcean mechanics and fault-injection workflow is documented
in [Linux HA acceptance lab](./ha-lab.md). The August 23, 2026 run restored an
isolated cluster from a full pgBackRest backup and archived WAL in a private,
versioned off-host Space, including a before/after recovery boundary. It also
proved active/passive Norn election, signed audit continuity, deployment from a
replicated private Git/SOPS catalog after promotion, and verified PostgreSQL TLS.
It does not prove active/active operation ownership, distributed socket
cancellation, multi-region database survival, or WORM retention.

## Stage Nomad and Consul security

Inspect the non-secret state and cutover phases:

```bash
norn host security plan
```

Generate private CAs, server/client certificates, reviewed HCL fragments, and a
client environment template without activating them:

```bash
norn host security init --address 10.0.0.10 --cert-days 365
```

Initialization writes a timestamped directory below
`~/.config/norn/host/security`. It does not edit managed agent configuration,
restart a process, bootstrap an ACL token, or edit the encrypted Norn API
environment. The bundle is built in a private temporary directory and published
only after it is complete. Private keys are mode `0600`; public certificates
are mode `0644`.
Move CA private keys to protected offline custody after all required node
certificates have been issued.

The macOS host generator creates a host-local stage. It is not a fleet PKI
issuer for a multi-server cluster; do not reuse one node's server certificate
across hosts or create incompatible per-host CAs. The Linux HA acceptance lane
has a separate controller-held fleet issuer and unique per-node identities.

The generated Consul fragments are deliberately split:

- `consul-transition.hcl` enables ACLs with `default_policy = "allow"` for the
  existing-datacenter bootstrap window;
- `consul-default-deny.hcl` is the final policy after server agents, client
  agents, Nomad service registration, and Norn each have scoped tokens.

Managed fragment activation remains intentionally unavailable for the
single-host macOS lane. Recovery, assurance, status, repair, and stopped-agent
probes honor the standard Nomad/Consul CA, client certificate, TLS server-name,
and ACL-token environment through a mode-`0600` active security environment.

The DigitalOcean Linux HA lane now automates the corresponding fleet exercise:

```bash
scripts/lab pki control-init
scripts/lab security-cutover cutover --confirm-disposable-lab
scripts/lab control-failover
scripts/lab security-cutover activate-production
```

Its Consul sequence installs trust and relaxed TLS, scopes client tokens, moves
every peer to outbound TLS, then requires inbound TLS and default-deny ACLs. Its
Nomad sequence uses `rpc_upgrade_mode`, bootstraps ACLs and workload identity,
then removes upgrade mode. Production activation is refused unless
`profile.production` is the only failed readiness check and is automatically
rolled back if convergence or the final zero-failure report fails.

For the authoritative substrate sequence, follow HashiCorp's
[Nomad TLS](https://developer.hashicorp.com/nomad/docs/secure/traffic/tls),
[Nomad ACL bootstrap](https://developer.hashicorp.com/nomad/docs/secure/acl/bootstrap),
[Consul TLS security model](https://developer.hashicorp.com/consul/docs/secure/security-model/core),
and [Consul ACL bootstrap](https://developer.hashicorp.com/consul/docs/secure/acl/bootstrap)
guides for the installed versions.

## Admission sequence

1. Run the readiness report and save the JSON result.
2. Build a three-server Nomad/Consul quorum and at least two Nomad clients.
3. Stage PKI and place client certificate paths in the SOPS-encrypted API
   environment.
4. Drain active Norn operations and important batch allocations.
5. Install and test the authenticated recovery/assurance environment.
6. Activate the fleet in the compatibility sequence: Consul relaxed TLS/ACL,
   scoped clients, outbound TLS, inbound TLS/default-deny; then Nomad RPC
   upgrade mode, ACL/workload-identity bootstrap, and final RPC enforcement.
   Never run Norn with bootstrap management tokens.
7. Stop each elected Consul and Nomad leader and require controller and workload
   continuity before enabling production admission.
8. Enable `NORN_PROFILE=production` only through a guarded, reversible rollout.
9. Run `norn production check`, deploy and restart a disposable app, exercise
   discovery and ingress, and complete a rollback and isolated restore drill.

## Remaining production gates

The HA lane now proves clean source, immutable registry identity, Cosign
publisher signatures bound to Git commits, Trivy HIGH/CRITICAL admission,
off-host versioned PITR, verified PostgreSQL TLS, and a checksum-verified
off-host audit export. It also proves Consul/Nomad TLS, default-deny ACLs,
workload identity, elected-leader failover, and production-profile activation.
The Space is versioned but not object-locked, so it is not WORM storage.
Scheduled export/drill execution, separate-account retention, multi-region
recovery, registry workload identity, protected CA custody/rotation, and
failure-domain separation remain production work.

Do not use a single macOS user-session runtime as the only failure domain for a
production SLO. The current launchd lane remains useful for development, edge,
and recovery testing; production quorum should use persistent Linux hosts and a
supervisor that starts before an interactive login.
