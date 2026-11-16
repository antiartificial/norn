# Norn v3 launch state

Status reviewed 2026-11-16 UTC. The DO staging **live online upgrade and
pilot retirement are complete**. The online upgrade passed on 2026-11-16;
exact-scope provider readback at 15:27 UTC confirmed retirement. Management
remains available for future Fleet work.

## Current evidence

| Item | Verified state | Limit |
| --- | --- | --- |
| Deployed platform | [Live run 43852466595](https://github.com/antiartificial/norn-fleet/actions/runs/43852466595) upgraded all three controls from signed fa301fa5 to cdf2081e through Fleet executor bdf522cc. Its 19-event receipt ended online-qualified. | This qualifies the existing Fleet platform upgrade, not fresh creation, workload resizing or every application. |
| Continuity | The accepted window contains 399 management-observer samples and 673 separate public probes with no observed workload outage, API handoff gap or sampling gap. API ownership moved Control 03 to Control 01. | Nine later public-probe timeouts occurred outside that window; their cause is unresolved. The last public probe succeeded and management observation remained policy-healthy. Do not describe the entire 45-minute monitor as uninterrupted. |
| Signed release and admission | cdf2081e passed [twelve qualification jobs](https://github.com/antiartificial/norn/actions/runs/43551678343) and [publication](https://github.com/antiartificial/norn/actions/runs/43552024315). [Exact-pair qualification 43851271049](https://github.com/antiartificial/norn-fleet/actions/runs/43851271049) and [state readback 43851955369](https://github.com/antiartificial/norn-fleet/actions/runs/43851955369) admitted the live operation. | Future operations still require fresh source, state, owner and signature bindings. |
| Recovery and final cleanup | Preparation-only recovery and the succeeding upgrade have retained evidence off-Fleet. | The controller saved its success receipt and cleared the active journal at 12:25 UTC; all three host-authority retirement receipts are retained. The worker and observer are stopped. [PR #1089](https://github.com/antiartificial/norn-fleet/pull/1089) repaired directory validation; [PR #1090](https://github.com/antiartificial/norn-fleet/pull/1090) repaired original-source Git ownership. These repairs are merged; the accepted upgrade was not rerun. |
| Pilot retirement | Owner-authorized exact-ID `doctl` retirement complete. Nine Fleet Droplets, both application database clusters, dedicated ingress load balancer, Fleet firewalls/VPC and management-to-Fleet peering are absent. Two management Droplets remain active, management database online; runner pinned SSH succeeds and authority is online on Tailscale. | The saved-plan retirement controller was not live-qualified. Spaces, state, evidence and credentials were retained; ordinary Fleet apply is disabled to prevent accidental recreation. Authority API health was not established by this readback. |
| Self-hosted strategy | The recorded direct-host deployment met the signed-V3/API/responding-workload bar on 2026-11-10. | Its repair and Git admission follow-ups have an independent lifecycle; Fleet resizing does not apply. |

## Pilot complete and follow-up boundary

The launch-critical online-upgrade and disposable-pilot retirement milestones
are complete. GitHub billing/capacity still blocks required CI for retirement
controller prerequisite [PR #1091](https://github.com/antiartificial/norn-fleet/pull/1091),
but no longer blocks this retired pilot. The owner explicitly chose direct
`doctl` cleanup. Do not treat that cleanup as saved-plan controller qualification.
Future creation requires deliberate state refresh, source review and plan
approval before re-enabling ordinary Fleet apply; retained state is historical.

Application nodes and workload sizes were unchanged during the platform
upgrade. Fresh-Fleet creation, workload admission, mixed-version resizing and
stateful compatibility continue after this pilot milestone.

See [release program milestones](release-program-milestones.md) and the
[Fleet retirement runbook](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/disposable-fleet-retirement-and-cost.md)
for the workstream overview and execution boundary.

<details>
<summary>Historical section links</summary>

The dated records below are archived evidence, not current launch gates.

- <a id="current-do-fleet-status-2026-11-12-1753-utc"></a>[Current DO Fleet status (2026-11-12 17:53 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#current-do-fleet-status-2026-11-12-1753-utc)
- <a id="managed-mysql-stateful-staging-candidate-2026-11-12-0341-utc"></a>[Managed MySQL stateful staging candidate (2026-11-12 03:41 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#managed-mysql-stateful-staging-candidate-2026-11-12-0341-utc)
- <a id="current-mini-status-2026-11-10-1526-utc"></a>[Current Mini status (2026-11-10 15:26 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#current-mini-status-2026-11-10-1526-utc)
- <a id="prior-do-v3-platform-release-and-controller-recovery-2026-11-11-0316-utc"></a>[Prior DO V3 platform release and controller recovery (2026-11-11 03:16 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#prior-do-v3-platform-release-and-controller-recovery-2026-11-11-0316-utc)
- <a id="do-staging-provider-inventory-2026-11-10-1458-utc"></a>[DO staging provider inventory (2026-11-10 14:58 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-staging-provider-inventory-2026-11-10-1458-utc)
- <a id="do-staging-provider-refresh-2026-11-10-1602-utc"></a>[DO staging provider refresh (2026-11-10 16:02 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-staging-provider-refresh-2026-11-10-1602-utc)
- <a id="do-bootstrap-controller-admission-merged-2026-11-10-1616-utc"></a>[DO bootstrap controller admission merged (2026-11-10 16:16 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-bootstrap-controller-admission-merged-2026-11-10-1616-utc)
- <a id="do-management-runner-readiness-2026-11-10-1620-utc"></a>[DO management runner readiness (2026-11-10 16:20 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-management-runner-readiness-2026-11-10-1620-utc)
- <a id="do-management-mac-controller-check-2026-11-10-1641-utc"></a>[DO management Mac controller check (2026-11-10 16:41 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-management-mac-controller-check-2026-11-10-1641-utc)
- <a id="do-gate-install-and-controller-postflight-2026-11-10-1720-utc"></a>[DO gate install and controller postflight (2026-11-10 17:20 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-gate-install-and-controller-postflight-2026-11-10-1720-utc)
- <a id="do-protected-state-object-readback-2026-11-10-1808-utc"></a>[DO protected state-object readback (2026-11-10 18:08 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-protected-state-object-readback-2026-11-10-1808-utc)
- <a id="do-genesis-readback-follow-up-2026-11-10-2001-utc"></a>[DO Genesis readback follow-up (2026-11-10 20:01 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-genesis-readback-follow-up-2026-11-10-2001-utc)
- <a id="do-genesis-readback-completed-2026-11-10-2048-utc"></a>[DO Genesis readback completed (2026-11-10 20:48 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-genesis-readback-completed-2026-11-10-2048-utc)
- <a id="do-norn-release-state-revalidated-2026-11-10-2053-utc"></a>[DO Norn release state revalidated (2026-11-10 20:53 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-norn-release-state-revalidated-2026-11-10-2053-utc)
- <a id="release-timing"></a>[Release timing](LAUNCH-STATE-ARCHIVE-2026-11-12.md#release-timing)
- <a id="historical-snapshot-2026-11-10-1050-utc-superseded"></a>[Historical snapshot (2026-11-10 10:50 UTC; superseded)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#historical-snapshot-2026-11-10-1050-utc-superseded)
- <a id="mini-queued-upgrade-recovery-gate-correction-2026-11-10-1050-utc"></a>[Mini queued upgrade recovery-gate correction (2026-11-10 10:50 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-queued-upgrade-recovery-gate-correction-2026-11-10-1050-utc)
- <a id="mini-direct-v3-platform-upgrade-completed-2026-11-10-0925-utc"></a>[Mini direct V3 platform upgrade completed (2026-11-10 09:25 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-direct-v3-platform-upgrade-completed-2026-11-10-0925-utc)
- <a id="mini-direct-v3-generation-finalized-2026-11-10-0630-utc"></a>[Mini direct V3 generation finalized (2026-11-10 06:30 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-direct-v3-generation-finalized-2026-11-10-0630-utc)
- <a id="fresh-mini-read-only-postflight-2026-11-10-08000809-utc"></a>[Fresh Mini read-only postflight (2026-11-10 08:00–08:09 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#fresh-mini-read-only-postflight-2026-11-10-08000809-utc)
- <a id="historical-mini-forward-upgrade-attempts-exposed-archived-receipt-lookup-2026-11-10-0859-utc"></a>[Historical: Mini forward-upgrade attempts exposed archived receipt lookup (2026-11-10 08:59 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#historical-mini-forward-upgrade-attempts-exposed-archived-receipt-lookup-2026-11-10-0859-utc)
- <a id="historical-mini-finalizer-repair-qualified-exact-release-awaiting-protected-publish-2026-11-10-0607-utc"></a>[Historical: Mini finalizer repair qualified; exact release awaiting protected publish (2026-11-10 06:07 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#historical-mini-finalizer-repair-qualified-exact-release-awaiting-protected-publish-2026-11-10-0607-utc)
- <a id="mini-finalizer-controller-mismatch-found-same-generation-still-pending-2026-11-10-0531-utc"></a>[Mini finalizer controller mismatch found; same generation still pending (2026-11-10 05:31 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-finalizer-controller-mismatch-found-same-generation-still-pending-2026-11-10-0531-utc)
- <a id="current-mini-recovery-status-2026-11-10-0348-utc"></a>[Current Mini recovery status (2026-11-10 03:48 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#current-mini-recovery-status-2026-11-10-0348-utc)
- <a id="mini-partial-authenticated-inventory-refresh-2026-11-10-0456-utc"></a>[Mini partial authenticated inventory refresh (2026-11-10 04:56 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-partial-authenticated-inventory-refresh-2026-11-10-0456-utc)
- <a id="current-do-staging-fleet-evidence-2026-11-10-0925-utc"></a>[Current DO staging Fleet evidence (2026-11-10 09:25 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#current-do-staging-fleet-evidence-2026-11-10-0925-utc)
- <a id="mini-direct-v3-upgrade-status-baseline-launched-2026-11-07"></a>[Mini direct V3 upgrade status (baseline launched 2026-11-07)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-direct-v3-upgrade-status-baseline-launched-2026-11-07)
- <a id="historical-v3-release-readiness-snapshot-2026-11-10-0152-utc"></a>[Historical V3 release readiness snapshot (2026-11-10 01:52 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#historical-v3-release-readiness-snapshot-2026-11-10-0152-utc)
- <a id="historical-mini-runtime-evidence-snapshot-2026-11-09-2247-utc"></a>[Historical Mini runtime evidence snapshot (2026-11-09 22:47 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#historical-mini-runtime-evidence-snapshot-2026-11-09-2247-utc)
- <a id="prior-minido-runtime-evidence-snapshot-2026-11-09-2208-utc"></a>[Prior Mini/DO runtime evidence snapshot (2026-11-09 22:08 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#prior-minido-runtime-evidence-snapshot-2026-11-09-2208-utc)
- <a id="fresh-mini-inventory-2026-11-07-2138-utc"></a>[Fresh Mini inventory (2026-11-07 21:38 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#fresh-mini-inventory-2026-11-07-2138-utc)
- <a id="read-only-mini-refresh-2026-11-08-1541-utc"></a>[Read-only Mini refresh (2026-11-08 15:41 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#read-only-mini-refresh-2026-11-08-1541-utc)
- <a id="read-only-refresh-2026-11-08-1812-utc"></a>[Read-only refresh (2026-11-08 18:12 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#read-only-refresh-2026-11-08-1812-utc)
- <a id="post-m8-protected-evidence-2026-11-07"></a>[Post-M8 protected evidence (2026-11-07)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#post-m8-protected-evidence-2026-11-07)
- <a id="mini-in-place-upgrade-checkpoint-historical-observation-2026-11-07"></a>[Mini in-place upgrade checkpoint (historical observation, 2026-11-07)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-in-place-upgrade-checkpoint-historical-observation-2026-11-07)
- <a id="do-staging-fleet-read-only-refresh-2026-11-08-1545-utc"></a>[DO staging Fleet read-only refresh (2026-11-08 15:45 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#do-staging-fleet-read-only-refresh-2026-11-08-1545-utc)
- <a id="fleet-running-upgrade-checkpoint-2026-11-02-0010-utc"></a>[Fleet running-upgrade checkpoint (2026-11-02 00:10 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#fleet-running-upgrade-checkpoint-2026-11-02-0010-utc)
- <a id="fleet-staging-canary-checkpoint-2026-11-01-2349-utc"></a>[Fleet staging canary checkpoint (2026-11-01 23:49 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#fleet-staging-canary-checkpoint-2026-11-01-2349-utc)
- <a id="mini-adoption-evidence-checkpoint-historical-2026-11-01-0533-utc"></a>[Mini adoption evidence checkpoint (historical, 2026-11-01 05:33 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-adoption-evidence-checkpoint-historical-2026-11-01-0533-utc)
- <a id="operating-lanes-and-api-retirement-boundary-historical-pre-upgrade-snapshot-2026-11-07"></a>[Operating lanes and API retirement boundary (historical pre-upgrade snapshot, 2026-11-07)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#operating-lanes-and-api-retirement-boundary-historical-pre-upgrade-snapshot-2026-11-07)
- <a id="track-boundaries-2026-10-11"></a>[Track boundaries (2026-10-11)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#track-boundaries-2026-10-11)
- <a id="latest-mini-transition-checkpoint-2026-10-11-2100-utc"></a>[Latest Mini transition checkpoint (2026-10-11 21:00 UTC)](LAUNCH-STATE-ARCHIVE-2026-11-12.md#latest-mini-transition-checkpoint-2026-10-11-2100-utc)
- <a id="current-decision"></a>[Current decision](LAUNCH-STATE-ARCHIVE-2026-11-12.md#current-decision)
- <a id="controller-and-executor-state"></a>[Controller and executor state](LAUNCH-STATE-ARCHIVE-2026-11-12.md#controller-and-executor-state)
- <a id="mini-and-source"></a>[Mini and source](LAUNCH-STATE-ARCHIVE-2026-11-12.md#mini-and-source)
- <a id="pilot-lineage"></a>[Pilot lineage](LAUNCH-STATE-ARCHIVE-2026-11-12.md#pilot-lineage)
- <a id="milestone-estimates"></a>[Milestone estimates](LAUNCH-STATE-ARCHIVE-2026-11-12.md#milestone-estimates)
- <a id="next-actions"></a>[Next actions](LAUNCH-STATE-ARCHIVE-2026-11-12.md#next-actions)
- <a id="evidence-locations"></a>[Evidence locations](LAUNCH-STATE-ARCHIVE-2026-11-12.md#evidence-locations)

</details>
