# Mini legacy-baseline environment readiness — 2026-09-25

This was a read-only comparison of variable **names** in the live Mini API
SOPS JSON, LaunchAgent plist, launcher, launchd environment, and release
configuration against the v3 candidate and `legacy-baseline` command. No
secret values or live configuration were changed.

The installed API remains release
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. Its SOPS JSON contains
`NORN_API_TOKEN` and the existing application/S3/Cloudflare settings. It does
not contain `NORN_DATABASE_URL`, `NORN_AUDIT_SIGNING_KEY`, `NORN_PROFILE`, or
`NORN_ENVIRONMENT`. The LaunchAgent plist declares no environment variables,
and `launchctl getenv` found these names unset. The launcher supplies
`PGHOST`, `PGUSER`, and `PGPASSWORD` defaults, while v3's config reads its own
default PostgreSQL URL when `NORN_DATABASE_URL` is absent. The release config
has release fetch/verify/signature-policy variable names, but it does not
supply the control database URL or audit key.

## Exact transition needed before the one-way fence

| Gate | Current read-only finding | Required preparation |
| --- | --- | --- |
| Backup proof identity | `NORN_DATABASE_URL` and `NORN_AUDIT_SIGNING_KEY` are absent from the API SOPS and launchd environments. `legacy-baseline` rejects the proof before build without both. | Provision one stable, at least 32-byte runtime audit key and an explicit URL for the same exact control database in the protected API environment. Run the maintenance command with those **same bytes** loaded; textual URL changes alter the HMAC identity even if they reach the same server. Do not reuse the ephemeral key from the generated-proof rehearsal. |
| Protected backup | The 2026-09-25 generated rehearsal dump and proof were deleted. | Create an independently retained, owner-only backup and mode-`0600` proof bound to the installed legacy SHA, the stable key, and the exact URL. Verify SHA-256, byte count, source, age, and a private restore before the maintenance window. |
| Maintenance command | Both Mini's managed `platform-upgrade` and live checkout script lack `legacy-baseline`. | Stage and verify the reviewed exact candidate script and signed release artifacts before the window; select that script explicitly. Do not let the older managed or checkout script select the upgrade lane. |
| Drain and owner fence | `NORN_API_TOKEN` is named in SOPS, and the direct Mini LaunchAgent and release link exist. Current active operations and listener ownership were not qualified for a future window. | Load the authenticated token through the protected environment, set `NORN_DRAIN_MODE=fail`, prove zero active operations and the sole launchd-owned loopback listener immediately before fencing. |

The `platform-upgrade env-exec` helper loads API SOPS values for a child
command; a direct `legacy-baseline` shell invocation does not decrypt SOPS on
its own. The API launcher and maintenance command must use one intentionally
shared configuration after preparation. Merely making a proof with a temporary
signing key would pass a disposable rehearsal but fail to establish the
future runtime's audit/signature identity.

## Production profile is a separate cutover

Mini currently defaults to development profile/environment. Switching the v3
candidate to `NORN_PROFILE=production` during the one-way baseline would add
startup requirements not met by the current variable set: keyless release
admission and attestation issuer/repository/workflow/SBOM policy;
`NORN_REQUIRE_EXPLICIT_AUTH=true`; verified HTTPS Nomad and Consul endpoints;
a PostgreSQL URL using `sslmode=verify-full`; `NORN_REGISTRY_URL`; and a
32-byte audit key. If `NORN_ENVIRONMENT=production` is also selected, startup
requires trusted qualification public keys and explicit GitHub Actions OIDC
audience, repository, workflow, ref, event, app, environment, and default-branch
allowlists. Those names are absent from the live SOPS JSON. The API token name
is present, but a names-only review does not prove its length or scope.

These production settings require their own TLS, release-attestation, and
substrate proof. The guarded legacy-baseline path can be prepared with Mini's
existing profile while keeping production admission closed; that does not
turn a successful schema transition into production qualification.
