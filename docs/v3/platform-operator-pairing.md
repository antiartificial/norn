# Temporary platform operator pairing for Fleet recovery

The etcd normal Fleet runtime exposes a separate pairing lane only when the
existing platform release intent lane is enabled. That gate defaults off.
There is no additional enrollment flag or config transition required after a
signed Norn release containing this route.
The lane uses the existing trusted pairing transport, durable etcd identity
registry and managed admin approval. It admits only platform
`fleet-platform-operator` with the single scope `fleet:deployment-recover`.
The device name must be `fleet-deployment-recovery:<canonical-operation-uuid>`;
that value becomes the managed token subject, and the recovery endpoint
requires it to match the operation ID in the URL. The token cannot perform
general `platform:operate` actions.
The issued token expires after 30 minutes, cannot be rotated, and is revoked
when the admin deletes the device.

Paths:

- `POST /api/v1/fleet/platform-operator/enrollments`: start pairing.
- `GET /api/v1/fleet/platform-operator/enrollments`: admin-only status.
- `POST /api/v1/fleet/platform-operator/enrollments/approve`: admin approval.
- `POST /api/v1/fleet/platform-operator/enrollments/{id}/exchange`: exchange the verifier.
- `GET /api/v1/fleet/platform-operator/devices`: admin-only registry read.
- `DELETE /api/v1/fleet/platform-operator/devices/{id}`: admin-only revocation.

The controller that uses this lane must first promote the signed Norn release
containing it to all three controls and verify the exact live source and
healthy etcd pool. It must revoke the operator device after recovery and
terminal proof, and revoke the temporary admin token. A failed or ambiguous
attempt holds for registry and operation readback before retry.
