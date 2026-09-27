# Mini declared but inactive job classification — 2026-09-26

Read-only Mini source and Nomad API inspection covered the four exceptions
from the authenticated M0 inventory. No job, source file, database, or API
state was changed. Source inspection emitted only app/process names, the
`deploy` flag, short Git revision, and dirty-path count; it did not copy the
dirty files or print environment values.

| App | Current source | Nomad base job | Allocation/evaluation evidence | Fixture disposition |
| --- | --- | --- | --- | --- |
| `ad-asset-verifier` | `deploy: true`, web process; Git `3984642`, one dirty path | Absent, zero job versions | No allocation under this job ID | Declared-only fixture record; do not infer a running workload or deploy it automatically. |
| `ft-trove` | `deploy: true`, web process; Git `32a983f`, four dirty paths | Absent, zero job versions | No allocation under this job ID | Declared-only fixture record. This is a different app from running `like-trove`. |
| `hello-norn` | `deploy: true`, web process; Git `47b77f5`, two dirty paths | Absent, zero job versions | No allocation under this job ID | Declared-only fixture record. |
| `its-alive-api` | `deploy: true`, web process; Git `89d6bf6`, three dirty paths | Dead, two job versions, `Stop=false` | No current allocations or evaluations from the Nomad job endpoints | Preserve the dead-job state in the representative fixture; do not translate it into healthy service proof. |

The first three names returned no base job, job-version history, or job
allocations from the inspected Nomad endpoints. `its-alive-api` retained a
job definition but no current allocation or evaluation. Nomad's current
absence does not establish whether any of these were deliberately disabled,
failed earlier, or are awaiting deployment. `deploy: true` is source intent,
not observed running state. The dirty checkout counts make a clean-revision
fixture insufficient without owner review of the intended source content.

## Upgrade handling

Keep all four app declarations in source/inventory compatibility checks.
Record the three absent jobs and the dead job as separate observed-state
classes in M5 preflight and postflight. Do not create, restart, or repair them
as a side effect of the control-plane upgrade. Before M0 sign-off, an owner
must decide whether each app should remain declared-only, be repaired in a
separate operation, or be removed from desired source through its own change.
That decision also determines whether it belongs in an active-workload smoke
fixture. The v3 release rehearsal must preserve the chosen state, rather than
assume every `deploy: true` app is currently running.
