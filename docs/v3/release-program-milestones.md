# V3 release program milestones

Status reviewed 2026-11-16 UTC. The DO **online-upgrade and pilot-retirement
milestones are complete**. Upgrade and terminal cleanup succeeded on 2026-11-16;
provider readback at 15:27 UTC confirmed exact Fleet absence with management
retained. The self-hosted deployment met its recorded acceptance bar on 2026-11-10
and has an independent lifecycle.

## Launch milestones

| Milestone | State | Exit evidence |
| --- | --- | --- |
| Publish the qualified candidate | Complete | Signed cdf2081e passed [twelve qualification jobs](https://github.com/antiartificial/norn/actions/runs/43551678343) and [immutable publication](https://github.com/antiartificial/norn/actions/runs/43552024315). |
| Qualify and admit the exact pair | Complete for the live executor | [Compatibility 43851271049](https://github.com/antiartificial/norn-fleet/actions/runs/43851271049), protected owner-private [state readback 43851955369](https://github.com/antiartificial/norn-fleet/actions/runs/43851955369), fresh active-owner evidence and a signed intent admitted the actual upgrade on Fleet executor bdf522cc. |
| Recover the earlier preparation hold | Complete | The controller fenced the held attempt before member invocation, proved all controls still on the old signed release, retained evidence and retired its runner. The succeeding upgrade used a fresh intent. This recovery was not itself upgrade qualification. |
| Accept the DO online upgrade | Complete, 2026-11-16 | [Live run 43852466595](https://github.com/antiartificial/norn-fleet/actions/runs/43852466595) ended online-qualified. All three controls were verified on cdf2081e, passive controls first and the API owner last. API ownership moved Control 03 to Control 01. The accepted window recorded 399 observer samples and 673 separate public probes without observed workload outage, API handoff gap or sampling gap. |
| Finish terminal controller cleanup | Complete, 2026-11-16 | [Fleet PR #1089](https://github.com/antiartificial/norn-fleet/pull/1089) repaired handoff-directory validation and installed a separately pinned adapter. [PR #1090](https://github.com/antiartificial/norn-fleet/pull/1090) repaired Git trust for the verified original and repair checkouts. Both are merged. The controller saved terminal-and-runner-retired success at 12:25 UTC, cleared its active journal, and retained all three host-authority retirement receipts. Worker and observer are stopped. The accepted upgrade was not rerun. |
| Retire disposable staging | Complete, 2026-11-16 | Owner-authorized exact-ID `doctl` deletion followed by provider readback: nine Fleet Droplets, two application databases, dedicated ingress load balancer, Fleet firewalls/VPC and Fleet-to-management peering absent. Two management Droplets active, management database online, runner reachable over pinned SSH, authority online on Tailscale. Spaces, state/evidence and credentials retained; ordinary Fleet apply disabled. This is direct retirement evidence, not live qualification of the saved-plan retirement controller. |

The longer 45-minute public monitor recorded nine timed-out probes after the
accepted upgrade window, among 2,679 probes. Its last probe and a fresh public
check succeeded; management observation remained policy-healthy. Their cause
is unresolved. Retain them separately from the zero-failure upgrade window.

The owner chose direct `doctl` retirement while GitHub billing/capacity blocks
required CI for [Fleet PR #1091](https://github.com/antiartificial/norn-fleet/pull/1091).
That prerequisite and live qualification of saved-plan retirement remain
follow-ups; they do not reopen this completed pilot. Authority API health and
billing settlement were not established by the final infrastructure readback.

## Parallel workstreams

| Workstream | Current evidence | Next proof |
| --- | --- | --- |
| Existing DO platform | Live online upgrade 43852466595 accepted on 2026-11-16; all three controls verified on signed cdf2081e with workload continuity. | Pilot retired with management retained. Future recreation needs deliberate refresh and reviewed plan before ordinary apply is re-enabled. |
| Self-hosted direct | Recorded deployment acceptance bar met on 2026-11-10. Self-hosted operation is independent of the Fleet. | Separate repair and Git-driven workload admission follow-ups. No Fleet resizing. |
| V3 workload admission | Negative admission, durable replay, and offline input/provenance checks exist. Current source and tests do not establish a live controller-admitted canary receipt. | Verify protected inputs and credential provenance, then bind a responding workload to durable same-intent admission receipts. This follows the platform launch. |
| Fresh-Fleet creation | General and Stateful roles, topology checks, authority models, and default-off API work exist. The published candidate includes the new source. No fresh provider creation is qualified. | Connect one-use grants to the provider executor and protected workflow. Qualify partial inventory, ambiguous effects, recovery, and bootstrap. Creation may stay undeployed until needed. |
| Workload resizing | Disposable real-Nomad and etcd-backed recovery checks and mixed-version capacity models exist. No live DO resize is qualified. | Complete real executor, health, ingress, and old-cohort-stop proof, then qualify a stateless DO resize. Keep stateful compatibility separate. |
| Controller recovery | The earlier preparation hold was recovered before any member invocation. The succeeding live upgrade has retained signed intent, authenticated artifact, journal and observation evidence. | Terminal handoff and worker/observer cleanup are complete. Keep offline crash models distinct from live fault-injection proof. |
| V2-only retirement | Cleanup follows successful V3 adoption. | Remove unused V2 code and tests while preserving versioned contracts still used by V3. |

## Evidence and operating detail

[Launch state](LAUNCH-STATE.md) is the current status reference. The
[Fleet readiness record](https://github.com/antiartificial/norn-fleet/blob/main/docs/v3-release-readiness-milestones-2026-11-08.md)
tracks implementation evidence. [Execution milestones](execution-milestones.md)
retain the broader feature program. The
[release runbook](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/work-staging-norn-release.md)
contains procedures. [Launch history](LAUNCH-HISTORY.md) and the archive below
preserve dated results and failed attempts.
Use the [executor qualification rubric](executor-qualification-rubric.md)
when reviewing controller evidence. It distinguishes model, component,
disposable, recovery, and live proof without changing these milestone states.

The complete program still requires verified input contracts, offline failure
coverage, live readiness checks, controller receipts, postflight, and recovery
for every supported lane. Completing the narrower online platform launch does
not complete those follow-on workstreams.


<details>
<summary>Historical section links</summary>

The dated records below are archived evidence, not current launch gates.

- <a id="reuse-release-lessons-before-repeating-work"></a>[Reuse release lessons before repeating work](release-program-milestones-archive-2026-11-12.md#reuse-release-lessons-before-repeating-work)
- <a id="current-gate-snapshot-provider-inventory-and-platformcanary-endpoints-refreshed-2026-11-12-1753-utc-names-only-environment-audit-and-latest-30-workflow-history-query-refreshed-1725-utc-local-restart-qualifications-refreshed-1715-utc-statechart-suite-rerun-at-1735-utc-fleet-readback-runbook-pr-1028-merged-as-11e4611c-at-1654-utc-norn-restart-evidence-pr-450-merged-as-ae28d8bb-at-1655-utc-lane-guide-pr-451-merged-as-239cd8de-at-1701-utc-statechart-evidence-pr-454-merged-as-2852307d-at-1743-utc-and-milestone-timestamp-pr-455-merged-as-3d9798c8-at-1749-utc-no-release-publication-or-deployment-action-ran-for-these-documentation-updates"></a>[Current gate snapshot (provider inventory and platform/canary endpoints refreshed 2026-11-12 17:53 UTC; names-only environment audit and latest-30 workflow-history query refreshed 17:25 UTC; local restart qualifications refreshed 17:15 UTC; statechart suite rerun at 17:35 UTC; Fleet readback runbook PR #1028 merged as `11e4611c` at 16:54 UTC; Norn restart-evidence PR #450 merged as `ae28d8bb` at 16:55 UTC, lane guide PR #451 merged as `239cd8de` at 17:01 UTC, statechart evidence PR #454 merged as `2852307d` at 17:43 UTC, and milestone timestamp PR #455 merged as `3d9798c8` at 17:49 UTC; no release publication or deployment action ran for these documentation updates)](release-program-milestones-archive-2026-11-12.md#current-gate-snapshot-provider-inventory-and-platformcanary-endpoints-refreshed-2026-11-12-1753-utc-names-only-environment-audit-and-latest-30-workflow-history-query-refreshed-1725-utc-local-restart-qualifications-refreshed-1715-utc-statechart-suite-rerun-at-1735-utc-fleet-readback-runbook-pr-1028-merged-as-11e4611c-at-1654-utc-norn-restart-evidence-pr-450-merged-as-ae28d8bb-at-1655-utc-lane-guide-pr-451-merged-as-239cd8de-at-1701-utc-statechart-evidence-pr-454-merged-as-2852307d-at-1743-utc-and-milestone-timestamp-pr-455-merged-as-3d9798c8-at-1749-utc-no-release-publication-or-deployment-action-ran-for-these-documentation-updates)
- <a id="ordered-gates"></a>[Ordered gates](release-program-milestones-archive-2026-11-12.md#ordered-gates)
- <a id="m0-freeze-the-present-tense-evidence"></a>[M0. Freeze the present-tense evidence](release-program-milestones-archive-2026-11-12.md#m0-freeze-the-present-tense-evidence)
- <a id="m1-close-lane-and-input-contracts"></a>[M1. Close lane and input contracts](release-program-milestones-archive-2026-11-12.md#m1-close-lane-and-input-contracts)
- <a id="m2-build-deterministic-offline-fitness"></a>[M2. Build deterministic offline fitness](release-program-milestones-archive-2026-11-12.md#m2-build-deterministic-offline-fitness)
- <a id="m3-re-establish-do-controller-readiness-and-prove-live-state"></a>[M3. Re-establish DO controller readiness and prove live state](release-program-milestones-archive-2026-11-12.md#m3-re-establish-do-controller-readiness-and-prove-live-state)
- <a id="m4-qualify-the-existing-do-platform-controller"></a>[M4. Qualify the existing DO platform controller](release-program-milestones-archive-2026-11-12.md#m4-qualify-the-existing-do-platform-controller)
- <a id="m5-qualify-workload-resize-and-application-overlap"></a>[M5. Qualify workload resize and application overlap](release-program-milestones-archive-2026-11-12.md#m5-qualify-workload-resize-and-application-overlap)
- <a id="m6-decide-on-online-norn-platform-upgrades"></a>[M6. Decide on online Norn platform upgrades](release-program-milestones-archive-2026-11-12.md#m6-decide-on-online-norn-platform-upgrades)
- <a id="m7-remove-v2-runtime-surfaces-after-adoption"></a>[M7. Remove V2 runtime surfaces after adoption](release-program-milestones-archive-2026-11-12.md#m7-remove-v2-runtime-surfaces-after-adoption)
- <a id="dependencies-and-concurrency"></a>[Dependencies and concurrency](release-program-milestones-archive-2026-11-12.md#dependencies-and-concurrency)
- <a id="program-completion"></a>[Program completion](release-program-milestones-archive-2026-11-12.md#program-completion)

</details>
