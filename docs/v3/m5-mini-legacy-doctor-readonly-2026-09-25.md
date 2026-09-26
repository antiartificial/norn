# Mini legacy transition doctor — read-only 2026-09-25

At approximately 21:05 UTC, the current branch's
`legacy-baseline-doctor` ran read-only on Mini against candidate source
`568f671cc87cd7a3ffe90d81b7ac9539d43629b4` and installed legacy source
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. The authenticated token was
loaded in-process from Mini's existing SOPS file and was not printed. The
temporary doctor and verifier copies were removed afterward. No service,
database, release, or provider state was changed.

| Gate | Result | Evidence or next action |
| --- | --- | --- |
| Runtime database URL and audit key binding | Block | Neither value is present in the launcher SOPS environment or the maintenance invocation. Stage matching protected values before a production-key proof or transition. |
| Protected backup proof and artifact | Block | No separately retained production-key artifact was supplied. Create it from the exact legacy database, verify its proof, and restore that artifact privately. |
| Reviewed platform script | Block | No exact candidate script was supplied to the doctor. Bind the script bytes to the reviewed candidate commit. |
| Exact candidate release | Block | No immutable candidate release is installed on Mini. Publish and verify the signed exact-SHA release before maintenance. |
| Legacy release identity | Pass | Current release link and launch executable matched the declared legacy SHA. |
| Signed release pair | Block | The read-only verification command failed while the candidate release was absent. Recheck after installation; this result does not implicate the already signed legacy release. |
| Listener ownership | Pass | The direct loopback API listener was solely owned by the legacy launchd PID. |
| Operation drain | Pass | Authenticated active-operation count was exactly zero with fail-drain mode. |

The doctor returned `ready=false`. Its passing live checks are point-in-time
observations. The production-key backup, exact signed candidate, and scheduled
one-way fence remain required before a Mini upgrade. The fresh copied-data
rehearsal is recorded separately in
[M5 current-head private-copy rehearsal](m5-mini-current-head-private-copy-2026-09-25.md).

## Read-only Mini refresh — 2026-09-26 17:22 UTC

The standard authenticated Norn inventory ran against Mini without changing
services or provider state. The API reported `v2.20.0-platform-30-ga5da8ef`;
the current release resolved to source
`a5da8ef15d12e9eca7561e90b90d96f6dc652a21`. The API and host health
reported `ok`, active operations were zero, and Fleet had zero node pools and
zero plans. This confirms the installed release has not become the v3 candidate
since the doctor run. The inventory does not revalidate the doctor's launcher,
listener ownership, production-key, or backup checks.

The installed v2 production-readiness endpoint returned `blocked` with 7 pass,
1 warning, and 19 failed checks. Its failures include the production profile,
Nomad/Consul quorum and TLS, external database/PITR, offsite snapshots, and
recovery drills. Those checks describe the current single-host v2 substrate;
they are not a substitute for the M5 upgrade gate or the separate fresh Fleet
M3 qualification. The next M5 action remains binding the exact signed
candidate and protected production-key backup to the private upgrade/rollback
rehearsal, then rerunning the legacy-transition doctor against those artifacts.
