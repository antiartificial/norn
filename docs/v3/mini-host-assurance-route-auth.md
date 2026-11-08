# Mini host assurance route authentication

Mini is an independent node. Its host assurance runs as a user LaunchAgent and
must not store API credentials in the plist. Public-route reconciliation uses
`norn forge <app>`, which submits an authenticated operation to the control API.

## Credential handoff

`host-runtime` routes only `forge <app>` through the managed
`platform-upgrade env-exec` helper. That helper decrypts the existing SOPS API
environment for the child process. The child uses the managed host-runtime
binary and accepts exactly one app that appears once in the owner-managed
`assure-forge` file. Deploy and restart operations keep their existing runtime
mutation fence and are not admitted through this route-only child mode.

While the retained `candidate-postflight-failed` sentinel is active,
`platform-upgrade` permits this child only at the canonical
`$HOME/.config/norn/host/bin/host-runtime` path with the canonical managed
config directory. The exception does not admit arbitrary executables or
commands. It does not clear or rewrite the recovery sentinel.

The platform release artifact contains `host-runtime` and `platform-upgrade`.
Release activation copies the verified artifact versions into the managed host
tool directory, so the controller can update both scripts from the signed
candidate.

## Verification

The V3 CI gate runs `test-host-assurance-route-fence.py` and
`test-recovered-baseline-adopt.py`. These cover the runtime-mutation fence,
managed app allowlist, exact child mode, SOPS credential handoff, canonical
binary path, and rejection of arbitrary or redirected children.

`norn smoke platform` is read-only and does not run host assurance. A successful
smoke does not prove public routes. After a protected release installs the
scripts, a controlled `norn host assure` run may publish or prune configured
Cloudflare routes. Verify its fresh assurance event and route inventory before
recording route recovery. Never treat the old incident as repaired based only
on a successful API smoke.
