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
