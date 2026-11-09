# Norn v3 launch state

**As of 2026-11-09 03:30 UTC.** This page is the short current-state index.
It is not a runbook or an authorization to dispatch. Follow each lane's
runbook for procedures; use [launch history](LAUNCH-HISTORY.md) for dated
observations and lessons. Recheck live state before acting.

## Current position

| Lane | Evidence now | Open gate |
| --- | --- | --- |
| Mini direct | Independent Mini runs signed V3 RC `d4dc88e0c37003a1013caed444983b2eeb50fbcb`. Read-only inventory at 2026-11-09 02:32 UTC reported API and host status `ok`. | Route assurance is not current: latest successful assurance is 2026-09-06. There are 25 incident groups and seven queued Fleet dispatch receipts. Norn PR [#301](https://github.com/antiartificial/norn/pull/301) fixes authenticated route assurance, but remains open and undeployed. Mini is not a Fleet member. |
| DO staging Fleet | Provider readback at 2026-11-09 02:48 UTC found nine active Droplets, active ingress load balancer, online staging MySQL/PostgreSQL, and the expected VPC. | Provider availability does not prove Norn runtime health. Protected Genesis readback [42418784411](https://github.com/antiartificial/norn-fleet/actions/runs/42418784411) failed because the installed source lacked pinned Git objects. Its one-use authorization is spent. |
| Genesis source admission | Fleet PR [#946](https://github.com/antiartificial/norn-fleet/pull/946) merged as `e75248407e5c49c8e306442b05cc0633059db6`; exact-head and merged-main contract checks passed. | The fix is not installed on the management host. The main staging plan [42555418513](https://github.com/antiartificial/norn-fleet/actions/runs/42555418513) succeeded read-only: 1 add, 0 change, 0 destroy. It proposes `management_ingress_runtime[0]`, allowing ports 18082/18083 on ingress nodes only from the management runner's private `/32`; the provider list at 03:24 UTC found no firewall with that name. Review the effect before any apply; none ran. Both protected dispatch gate variables were absent at 03:06 UTC. No new intent is authorized by this record. |
| DO controller docs and offline contract | Fleet PR [#944](https://github.com/antiartificial/norn-fleet/pull/944) records the gate state and current provider evidence. Its exact-head contract run [42554534060](https://github.com/antiartificial/norn-fleet/actions/runs/42554534060) passed all contract steps, including offline plan/reconciliation and executor recovery suites. Merged-main contract run [42555418496](https://github.com/antiartificial/norn-fleet/actions/runs/42555418496) passed on `e752484`. | After review of the separate provider-plan effect, install reviewed merged source with timers disabled, refresh all pins, and independently verify dormant state before requesting any fresh readback authorization. |
| Fresh Fleet bootstrap | Fleet offline war-game PR [#941](https://github.com/antiartificial/norn-fleet/pull/941), credential provenance PR [#943](https://github.com/antiartificial/norn-fleet/pull/943), and provider-scope contract PR [#945](https://github.com/antiartificial/norn-fleet/pull/945) are open. Future default is General + Stateful application pools. | These contracts do not deploy a Fleet or attest the live DO token's scopes. Complete source, credential lifecycle, sizing, placement, and recovery gates before any billable bootstrap. Existing DO staging remains separate and unchanged. |
| Workload release and resize | Norn PR [#302](https://github.com/antiartificial/norn/pull/302) has offline controller and statechart evidence for a stateless mixed-version release. | PR remains open and is not live-qualified. Operator admission, production-store crash/restart coverage, target-only public-route evidence, live canary, and stateful data compatibility remain open. Replica scaling and node-pool resizing are separate operations. |
| General online platform upgrade | The Mini's bounded forward upgrade completed through its direct controller, with recovery after a fencing race. | General online upgrade is unsupported. Do not weaken writer floors; prove writer compatibility/fencing, quorum, interruption, and restart recovery before changing the all-stop release protocol. |

## Next gates

1. Review the sole proposed firewall addition in provider plan [42555418513](https://github.com/antiartificial/norn-fleet/actions/runs/42555418513).
   The exact-head Fleet contract passed, while main contract validation
   [42555418496](https://github.com/antiartificial/norn-fleet/actions/runs/42555418496)
   remains active. Neither contract checks nor the provider plan are an apply
   or live Norn readback.
2. After main validation, install the merged Genesis source with controllers
   dormant. Verify its exact source, private inputs, digests, and dormant units;
   then refresh state, route, and host-key evidence. The old one-use intent
   cannot be reused.
3. Finish Mini route assurance through the reviewed direct release path and
   retain fresh post-assurance evidence. Do not conflate this with Fleet health.
4. Complete offline credential and failure war-games for fresh Fleet and
   workload release before opening their protected live gates.

## Reuse lessons before repeating work

- Check this page and [launch history](LAUNCH-HISTORY.md) before rerunning a
  scenario; use the source PR, exact-head CI, and live evidence to decide
  whether the previous result still applies.
- Keep provider inventory, control-plane health, workload health, route
  assurance, and release publication as separate claims.
- Preserve recovery evidence and writer floors. A successful readback, soak,
  model, or provider plan proves only the boundary it exercised.
- Keep detailed inventories, run IDs, and incident chronology in the history
  file or lane-specific evidence. Keep operator steps in the linked runbooks.

## Lane references

- [Release lanes and workload resizing](release-lanes-and-workload-resizing.md)
- [Release program milestones](release-program-milestones.md)
- [Release fitness and controller plan](release-fitness-and-controller-plan.md)
- [DO staging controller guide](https://github.com/antiartificial/norn-fleet/pull/944)
- [Fresh Fleet two-app-pool contract](fresh-fleet-two-app-pool-contract.md)
- [M5 workload release protocol](m5-workload-release-protocol-2026-11-07.md)
- [Online upgrade lessons](online-upgrade-lessons-2026-11-07.md)
