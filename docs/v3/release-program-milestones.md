# V3 release program milestones

Status reviewed 2026-11-14 UTC. The current launch gate is an online,
controller-mediated DO platform upgrade with measured workload continuity and
recoverable terminal evidence. The working target is the week of 2026-11-16.
The launch date remains conditional on that proof.

The existing DO Fleet is the disposable staging testbed. The independent Mini
already met its deployment success bar and does not gate this launch. Fresh
creation, workload admission, resizing, and stateful application work continue
after the online platform milestone.

## Launch milestones

| Milestone | State | Exit evidence |
| --- | --- | --- |
| Publish the qualified candidate | Complete | Signed `cdf2081e` passed [twelve qualification jobs](https://github.com/antiartificial/norn/actions/runs/43551678343) and [immutable publication](https://github.com/antiartificial/norn/actions/runs/43552024315). |
| Qualify source and joined runtime | Complete for components | Fleet `f482f21f` merged after contract CI. The [joined signed baseline/quorum proof](https://github.com/antiartificial/norn-fleet/actions/runs/43552764721) passed with complete cleanup. |
| Qualify the exact signed pair | Complete for plan storage and control fencing | [Producer 43553952979](https://github.com/antiartificial/norn-fleet/actions/runs/43553952979) passed for `fa301fa5` to `cdf2081e`. This does not establish live availability. |
| Exercise the full controller and restart | In progress | Real signed inputs, observer, SSH effects, same-operation restart, and terminal receipts in disposable guests. Synthetic admission metadata remains labelled fixture-only. |
| Install and preflight the controller | Pending | Exact installed source closure, protected files and digests, host keys, credentials, and fresh readback. The currently running management controller predates the online modules. |
| Accept the DO online upgrade | Pending, launch gate | Controller promotion, measured API handoff and workload continuity, recovery proof, and off-Fleet evidence retention. The live workflow stays disabled until its prerequisites pass. |
| Retire disposable staging | After accepted upgrade | Controlled teardown after required receipts and recovery inputs are retained elsewhere. No teardown has run for this work. |

## Parallel workstreams

| Workstream | Current evidence | Next proof |
| --- | --- | --- |
| Existing DO platform | Eleven staging Droplets were active in the work account on 2026-11-14. Public platform and canary responses returned 200. The last qualified promotion was the all-stop `fa301fa5` deployment. | Complete the online controller launch milestones above. |
| Mini direct | Owner success bar met on 2026-11-10. The Mini is independent of the Fleet. | Separate repair and Git-driven workload admission follow-ups. No Fleet resizing. |
| V3 workload admission | Negative admission, durable replay, and offline input/provenance checks exist. Current source and tests do not establish a live controller-admitted canary receipt. | Verify protected inputs and credential provenance, then bind a responding workload to durable same-intent admission receipts. This follows the platform launch. |
| Fresh-Fleet creation | General and Stateful roles, topology checks, authority models, and default-off API work exist. The published candidate includes the new source. No fresh provider creation is qualified. | Connect one-use grants to the provider executor and protected workflow. Qualify partial inventory, ambiguous effects, recovery, and bootstrap. Creation may stay undeployed until needed. |
| Workload resizing | Disposable real-Nomad and etcd-backed recovery checks and mixed-version capacity models exist. No live DO resize is qualified. | Complete real executor, health, ingress, and old-cohort-stop proof, then qualify a stateless DO resize. Keep stateful compatibility separate. |
| Controller recovery | Journals, fenced effects, independent observation, and offline recovery checks exist. Full joined runner restart and replacement-run adoption remain open. | Verify implementation through process loss and terminal replay. Preserve required inputs and keys through terminal evidence. |
| V2-only retirement | Cleanup follows successful V3 adoption. | Remove unused V2 code and tests while preserving versioned contracts still used by V3. |

## Evidence and operating detail

[Launch state](LAUNCH-STATE.md) is the current status reference. The
[Fleet readiness record](https://github.com/antiartificial/norn-fleet/blob/main/docs/v3-release-readiness-milestones-2026-11-08.md)
tracks implementation evidence. [Execution milestones](execution-milestones.md)
retain the broader feature program. The
[release runbook](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/work-staging-norn-release.md)
contains procedures. [Launch history](LAUNCH-HISTORY.md) and the archive below
preserve dated results and failed attempts.

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
