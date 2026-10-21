# Fleet platform release recovery contract

The runtime recovery API is disabled by default. It requires
`NORN_FLEET_PLATFORM_RELEASE_INTENT_ENABLED=true`,
`NORN_FLEET_PLATFORM_RECOVERY_ENABLED=true`, a pinned independent Ed25519
PKIX PEM at `NORN_FLEET_PLATFORM_RECOVERY_SUPERVISOR_PUBLIC_KEY_FILE`, and
`NORN_FLEET_PLATFORM_RECOVERY_SUPERVISOR_ID`. The supervisor key must differ
from both Genesis import authority keys. These settings expose recovery
authorization and takeover routes; they do not start an executor.

An independent root-owned supervisor must first stop the old executor and
confirm that its process, cgroup, and service unit are absent. It waits for
the old API lease and all issued remote promote grants to expire. It then
checks each pinned control member and the absence of an in-flight promote
helper before and after inspection. Its signed, canonical JSON attestation
binds the exact intent digest, attempt ID, old owner and session, fence epoch,
lease, supervisor identity, fresh observations in roster order, and a unique
recovery nonce. The signed evidence digest for each member covers the
supervisor's quiescence and inspection results.

The API accepts coherent healthy old or new members. A mixed API/UI source
or inactive service is accepted only when the same durable attempt has an
open `promote:begin` checkpoint for that member. This preserves the evidence
of an interrupted promotion for the next controller to repair; it does not
declare the member healthy. A later `promote:done` closes that exception.

An enrolled non-CI operator with `platform:operate` reviews the supervisor
artifact and posts its raw JSON and Ed25519 signature, base64 encoded, to
`POST /api/v1/fleet/platform-releases/{name}/attempt/recovery-authorizations`.
The API verifies the independent signature, freshness, roster, checkpoint
state, and expired lease before recording a one-use authorization. An enrolled
`fleet:execute` device consumes it with
`POST /api/v1/fleet/platform-releases/{name}/attempt/recover`, providing the
exact attempt ID, new owner ID and process session, old fence epoch, recovery
nonce, stop-attestation SHA256, and signed member-state SHA256. etcd compares
the old attempt and authorization revisions and advances the fence epoch in
one transaction. Stale owners cannot renew, checkpoint, or finish afterward.

The Fleet Genesis supervisor currently recovers the external SQLite Genesis
journal, before the target Norn API exists. The later runtime executor still
needs a recovery client that obtains the operator-approved authorization and
resumes from the signed member observations. Until that integration is tested,
keep this API recovery gate off in deployed environments.
