# Norn v3 launch state

Status reviewed 2026-11-15 UTC. The launch priority remains a controller-mediated
online upgrade of the existing DO staging Fleet while its workloads keep
serving. Any brief API interruption must stay within the signed observation
policy. The working target is the week of 2026-11-16, conditional on the
online-upgrade proof; no launch date is committed.

## Current evidence

| Item | Verified state | Limit |
| --- | --- | --- |
| DO staging | Protected read-only provider readback [run 43618560028](https://github.com/antiartificial/norn-fleet/actions/runs/43618560028) succeeded on Fleet main `24c1d64b`; the signed readback is bound to the current state and provider identities. | It does not prove workload continuity or online-upgrade behavior. |
| Deployed platform | The last qualified DO promotion deployed signed `fa301fa5` through the all-stop controller on 2026-11-11. | That promotion interrupted service. No online upgrade has been accepted. |
| Published candidate | `cdf2081e` passed [all twelve qualification jobs](https://github.com/antiartificial/norn/actions/runs/43551678343) and [immutable publication](https://github.com/antiartificial/norn/actions/runs/43552024315). The published Linux bundle was independently signature and hash verified. | Publication does not deploy the candidate. |
| Fleet controller source | Main `24c1d64b` is installed in the management controller. Its installed closure and protected configuration were verified; the online profile remains unbound. | The profile is not bound and the live online-release path has not been enabled. |
| Joined guest rehearsal | [Run 43552764721](https://github.com/antiartificial/norn-fleet/actions/runs/43552764721) ran signed `fa301fa5` against three native TLS/RBAC etcd members, with one active API and two passive controls. Every signed API/UI pin and all 44 UI files matched. Cleanup completed. | This is baseline and quorum evidence. It did not run the full signed upgrade controller. |
| Exact signed pair | [Producer run 43660225984](https://github.com/antiartificial/norn-fleet/actions/runs/43660225984) passed on frozen Fleet source `453d13d6` for `fa301fa5` to `cdf2081e`. Its authenticated artifact and all five local cryptographic/input checks are retained. | Fixture-only checks do not qualify full execution or live workload continuity. |
| Read-only release preflight | [Run 43618699939](https://github.com/antiartificial/norn-fleet/actions/runs/43618699939) succeeded with `preflight_only=true`. The fresh readback was 9 seconds old at signing/dispatch. Live etcd and component checks plus exactly one healthy private API passed. The durable terminal receipt confirms exact runner retirement. | The workflow correctly skipped the upgrade and all post-write steps. No release or provider mutation occurred; this does not qualify online upgrade. |
| Online-release candidate | [PR #1050](https://github.com/antiartificial/norn-fleet/pull/1050) merged as `453d13d6` after contract, signed runtime, systemd, readback, security and root Linux ownership checks passed. Local checks passed 500 online tests and 57 qualifier tests. [Full run 43660848131](https://github.com/antiartificial/norn-fleet/actions/runs/43660848131) completed preparation, then held at the first passive member stop task. One member invocation and an ambiguous effect are retained; all seven disposable resources were cleaned up. The source is not qualified for live execution. | Previous [run 43655236253](https://github.com/antiartificial/norn-fleet/actions/runs/43655236253) held in preparation with zero member invocations; its evidence and cleanup are retained. No live online upgrade has been accepted. |

## Launch completion steps

The remaining launch evidence is:

1. Qualify frozen controller/workflow source `453d13d6` while the live gate
   remains off. Bind the exact executor source with a matching signed pair, joined execution,
   completed-member restart/replay, independent observation, and retained
   terminal evidence. The refreshed controller candidate remains disabled and
   unqualified.
2. Bind the installed controller profile only after those prerequisites pass
   and the state/host evidence is freshly verified.
3. Prove the live online update on DO staging: passive controls first, API
   owner last, with measured private API handoff and the existing workload
   route responding through the update. Retain terminal receipts and recovery
   inputs across restart.
4. Retain accepted evidence outside the Fleet, then retire the staging Fleet
   through an exact-state, controller-mediated plan. Keep the separate management
   authority/runner and its database, state backends, and recreation credentials.
   The current retirement executor targets other disposable roots; the current
   staging root still needs its scoped retirement path.

The existing all-stop lane remains the available fallback, but it does not
meet the online-upgrade launch gate. Application nodes and workload sizes stay
unchanged during this platform release.

The owner accepts a successful controller-mediated update on this staging
Fleet as the pilot fitness result. Further workload soak, fresh-Fleet deployment
and resizing do not gate that evaluation or the subsequent Fleet retirement.
The configured retirement scope is nine Fleet Droplets and its two application
database clusters, plus owned load-balancer/network resources; the saved plan
and fresh inventory must establish the final deletion set.

## Other lanes

The independent Mini met its owner-defined success bar on 2026-11-10: signed
V3 ran, its API was healthy, and `vigil-gateway` responded. Mini repair and
Git-driven workload admission are follow-ups. The Mini has no Fleet resize lane.

Fresh-Fleet creation, V3 workload admission, mixed-version workload resizing,
stateful application compatibility, and V2-only cleanup remain separate
workstreams. Fresh Fleets default to General and Stateful application pools.
The current staging pool is not changed by that default.

The [release milestone map](release-program-milestones.md) tracks those
workstreams. The [Fleet readiness record](https://github.com/antiartificial/norn-fleet/blob/main/docs/v3-release-readiness-milestones-2026-11-08.md)
and [release runbook](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/work-staging-norn-release.md)
contain implementation and operating detail. Dated observations remain in
[launch history](LAUNCH-HISTORY.md) and the archive below.

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
