# Operate Fleet infrastructure

Fleet separates desired infrastructure, provider authority, and the application
control plane. The infrastructure repository and protected runner own cloud
changes. Norn exposes planning, reconciliation, node readiness, and drain state.

| Concern | Owner |
| --- | --- |
| Provider adapter, regions, VM sizes, networking, and desired pools | Infrastructure repository |
| Provider credentials, state backend, and provider effects | Protected infrastructure executor |
| Application identity, allocations, service readiness, and operational API | Norn and its runtime integrations |
| Release intent, independent observation, and recovery evidence | The operation's management/controller path |

## Prepare an environment

Set up protected source review, a versioned state backend, least-scoped provider
credentials, host trust, and the management/runner identity before provider
mutation. Run the infrastructure repository's setup/readiness tooling and review
its exact plan and source bindings.

DigitalOcean is the current adapter. Provider-neutral ownership boundaries do
not imply an adapter exists for another cloud. A new adapter needs inventory,
effect identity, readback, bootstrap, recovery, and retirement qualification.

## Inspect and plan

Against a configured Fleet control endpoint:

```sh
norn fleet validate /srv/norn-fleet/environments/staging/nyc3/cluster.yaml
norn fleet pools
norn fleet plan app --desired 4 --reason 'review capacity for release'
```

A capacity plan records intent. It is not provider apply and does not prove
workloads can drain safely. Source review, protected dispatch, actual node
readiness, and workload placement are later evidence boundaries.

The GitHub bridge can connect a plan to a deterministic review and protected
workflow. Provider and state credentials remain outside the ordinary Norn API.
Follow the infrastructure repository's runbook for the exact operation; do not
copy an older environment's IDs or credentials.

## Choose the release operation

| Goal | Procedure and limit |
| --- | --- |
| Update Norn on an existing compatible Fleet | [Online platform upgrade](online-upgrades.md). The recorded three-control rotation is qualified. |
| Create a new Fleet | Fresh creation needs its own protected inputs and qualification. The online-upgrade pilot did not qualify unattended creation. |
| Resize application pools or release mixed workload versions | Placement, remaining capacity, traffic and stateful compatibility need separate proof. No live DO resize was qualified by this pilot. |
| Retire a Fleet | Retain evidence, state and recreation inputs. Derive exact resource IDs and a preservation set, then verify deletion completion. The completed pilot used an owner-authorized direct cleanup; the saved-plan retirement controller remains unqualified live. |

The first pilot is retired. Its retained management and evidence are historical
release resources, not an environment readers should assume is running. See
[capability status](../reference/capabilities.md) and [release evidence](../release-program-milestones.md).
