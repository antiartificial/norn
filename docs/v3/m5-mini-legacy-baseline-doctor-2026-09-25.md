# Mini legacy-baseline read-only doctor — 2026-09-25

`v2/scripts/legacy-baseline-doctor` reports a JSON check list and exits nonzero
when any transition precondition is absent. It never fences a service, builds
or imports a release, migrates a database, or restarts an API. Run it with the
same protected maintenance environment that would invoke `legacy-baseline`:

```sh
NORN_DRAIN_MODE=fail \
v2/scripts/platform-upgrade env-exec -- \
  v2/scripts/legacy-baseline-doctor \
    --candidate-sha <reviewed-full-sha> \
    --legacy-release <installed-full-sha> \
    --backup-proof /absolute/private/control-proof.json \
    --backup-artifact /absolute/private/control.dump \
    --reviewed-script /absolute/reviewed/platform-upgrade \
    --artifact-verifier /absolute/reviewed/platform-release-artifact \
    --public-key /absolute/protected/release-signing.pub
```

It compares the exact `NORN_DATABASE_URL` and `NORN_AUDIT_SIGNING_KEY` bytes
in the launcher SOPS JSON with those in the maintenance process without
printing either value. The backup verifier checks the proof/artifact binding.
The doctor checks the reviewed script against the exact candidate Git commit,
the candidate release identity and startup contract. It requires an artifact
verifier whose bytes match the exact candidate Git commit and a pinned Ed25519
public key. It checks the signed manifest state and cryptographically verifies
the installed file inventory and signature of both exact releases. It also checks the current
release and launch executable, sole launchd ownership of the direct loopback
listener, that the running API process started after the launcher and active
SOPS file last changed, and that its mapped executable inode matches the
installed API file. This catches an activated SOPS file or replaced executable
that was not followed by an API restart; file equality alone cannot establish
the running process's binding. The process-start check is a conservative local
preflight rather than attestation of process memory. It also checks
`NORN_DRAIN_MODE=fail` and an authenticated zero active-operation count. Its
messages contain fixed, non-secret descriptions.

Fixture tests passed with `python3 v2/scripts/test-legacy-baseline-doctor.py`.
The new process-start predicate was also streamed to Mini and passed against
the current launcher process without writing a file or restarting the API.
The doctor was then streamed to Mini through SSH and run read-only with the
existing SOPS values loaded only in process. No file, service, or secret
configuration was changed on Mini. At that point it reported:

| Check | Result |
| --- | --- |
| Launcher/maintenance URL and audit key | Blocked: both required explicit values are absent. |
| Protected backup proof | Blocked: no retained artifact/proof was supplied. The earlier generated rehearsal artifact was deleted. |
| Reviewed script and exact candidate release | Blocked: the Mini checkout does not contain candidate `172042020cb059d9df146d37756a70637b4649bf`, the local script is older, and that release is not installed. |
| Signed candidate release | Blocked: the exact candidate is not installed for the trusted current verifier to check. |
| Installed legacy release and launch executable | Passed for `a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. |
| Direct listener owner | Passed: the loopback API listener had exactly the launchd service PID. |
| Authenticated operation drain | Passed: active operation count was zero at the time of this observation. |

The exit was nonzero, as required. A future passing doctor run is a
point-in-time preflight, not authorization to cross the one-way fence. Repeat
the checks inside the scheduled maintenance window and retain the separate
private restore and production backup evidence.
