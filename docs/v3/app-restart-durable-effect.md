# Durable app restart effect

`POST /api/v1/apps/{id}/restart` accepts a signed `app.restart` operation and
returns its durable operation receipt. The handler never calls Nomad.

The worker holds the operation generation claim, samples the active source
allocations, and persists their IDs, job identity, namespace, task group, and
Nomad create indexes in an `operation_effects` reservation before it sends the
first stop request. Each stop rechecks that exact allocation identity.

If a stop response is lost, the effect remains unresolved. Recovery reads only
the persisted source set. It does not use the job's current allocations as new
input and does not repeat any stop. It completes only after Nomad shows every
recorded source allocation terminal with a distinct successor in its recorded
lineage that is running. Missing, changed, pending, or otherwise unprovable
lineage remains `externalEffectRecoveryPending` for another reconciliation.

An expired restart claim is requeued so the durable effect can be reconciled.
The effect executor's reservation and execution identity bind work to the
claim generation, and a successor claim derives a different execution identity.
