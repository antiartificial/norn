# Recover a held Fleet deployment

Use this procedure after the recovery code is deployed and the failed deployment has a completed, healthy Nomad effect. An enrolled, non-CI device token with `platform:operate` is required. The endpoint requeues the same operation for one recovery attempt; it does not submit another Nomad job.

## Read-only checks

1. Confirm the running API and worker include the reviewed recovery code. A passing local test or merged change does not prove deployment.
2. Read `GET /api/v1/operations/{operationId}`. Confirm the operation is `app.deploy`, has status `failed`, and retains both `manualRecoveryRequired` and `externalEffectRecoveryPending`. Confirm its deployment ID and `effectId` match the intended receipt.
3. Inspect the completed effect and the exact Nomad job revision using the available read-only control and Nomad tools. Confirm the recorded execution identity and healthy running allocations. Confirm the previous route and public response still serve the expected release. Retain both jobs during recovery.
4. Confirm the signed Fleet target, ingress inventory, pinned source, and operator token are available. If any identity, effect, or route evidence differs, stop and investigate.

For the staging receipt recorded on 2026-11-01, the operation is `6d96db37-a54e-4231-b1b8-e3e616beb3ab` and the deployment is `1fbc30b3-3a3e-42f7-818d-48e732683cec`. Re-read their current state before use; these IDs alone do not authorize or prove readiness.

## Requeue and verify

Send an empty POST with the enrolled operator token:

```sh
curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${NORN_OPERATOR_TOKEN}" \
  "${NORN_API_URL}/api/v1/fleet/deployments/1fbc30b3-3a3e-42f7-818d-48e732683cec/operations/6d96db37-a54e-4231-b1b8-e3e616beb3ab/recovery"
```

An HTTP 202 means the held operation was queued. It does not mean the route changed or the deployment succeeded. The worker rechecks the exact Nomad job and its healthy allocations, publishes the route, and verifies traffic before it commits the active route and successful terminal receipt together.

Read the operation again and independently check the public response, ingress route, and Nomad job. A successful receipt has status `succeeded` and neither recovery hold. Verify the public response identifies the intended release before reporting success. Retire the old job only through a separate, reviewed operation.

A failed health check or other recovery error stops the attempt with a failed receipt and both holds retained. Inspect the failure before another explicit operator request. If the worker loses its lease, wait for expiry recovery to record the failed held receipt before retrying. Do not edit etcd records directly.
