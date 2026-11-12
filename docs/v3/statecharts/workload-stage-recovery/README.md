# Workload target staging and restart recovery

This bounded model checks the release controller from signed intent admission
through private target staging, one-use Nomad create, successor recovery,
target health, route publication, and completion. It focuses on the ambiguous
create window: after the durable attempt marker, a lost response or controller
crash must never cause a second create. The old route stays serving until an
exact target readback is bound and the target is healthy. The old workload can
drain only after target-only routing is proved; the terminal receipt follows
that drain.

Reproduce the model and chart from this directory:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/WorkloadStageRecovery.tla" \
  --config "$PWD/WorkloadStageRecovery.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-local"
```

The 2026-11-11 run explored 26 states and 31 transitions. It checked eight
rules: one create maximum, durable marker before create, exact readback before
binding, healthy bound target before routing, old drain only after target route
and health proof, terminal receipt after the drain, no completion with an
unresolved create, and no deadlock. The chart is in
[`reproduced-2026-11-11/chart.mmd`](reproduced-2026-11-11/chart.mmd), and TLC's report is
in [`reproduced-2026-11-11/report.md`](reproduced-2026-11-11/report.md).

The sequence matches the implementation boundaries in
`v2/api/etcd_workload_release_runtime.go` and
`v2/api/etcdstore/v3_workload_release_verified_integration_test.go`:

- `verifyAndStageWorkloadReleasePlan` reserves the effect and stages the
  accepted private input before recording the target create marker.
- `stageAndBindWorkloadReleaseTarget` permits one create-only Nomad request;
  the request result is not trusted as proof. The successor binds only after
  exact scheduler readback.
- `TestV3WorkloadReleaseStagedTargetBindingIsClaimFencedAndWriteOnce` checks
  pre-attempt reservation recovery, one-use marker behavior, successor claim,
  not-found hold, exact target binding, and rejection of stale claims.
- `TestWorkloadReleaseReconcilerRecoversRouteLostResponseAfterRestartEtcd`
  closes and reopens the etcd client, transfers the expired operation lease,
  and checks same-intent route recovery through the shared reconciler.
- `TestWorkloadReleaseStageAndRouteRecoverSameIntentAcrossEtcdRestarts`
  composes the reserved target-create boundary with route/drain reconciliation
  across successive client/store reconstructions. External effects use a test
  driver. After worker-style fenced terminalization, it reconstructs the store
  again and verifies the exact terminal receipt and succeeded operation replay.
- `TestWorkloadReleaseStageRecoversLostNomadResponseAfterEtcdRestart` sends the
  production target-stage helper to a disposable Docker-capable Nomad agent. A
  proxy drops Nomad's successful create response and hides the first readback;
  after an etcd client restart, the successor adopts the original reservation,
  binds Nomad's exact job version and modify index, and does not register a
  duplicate job.

Run the three restart qualifications with
`bash v2/scripts/test-workload-release-stage-nomad.sh`. The route/drain and
terminal-replay composition uses a test driver; the separate Nomad test proves
the target-stage create/readback recovery boundary against a real local Nomad
agent. They do not yet compose a real Nomad target through health, production
ingress, old-cohort drain, and terminal operation commit in one worker run. The
model also omits real Nomad ACLs and retention, request cancellation and delay
bounds, production ingress, placement/capacity, secret-key rotation, and live
DO traffic. An absent readback after the attempt marker remains unresolved:
the worker must keep reconciling or hold and must not resubmit. This is offline
safety and disposable-local qualification, not staging qualification.

The chart hides three same-label readback/reconciliation stutters and two
model actions that do not change a user-visible release phase. TLC still checks
those transitions and all rules.
