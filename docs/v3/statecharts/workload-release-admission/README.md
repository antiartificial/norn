# Workload release admission and recovery model

This bounded TLA+ model describes the proposed M5 release-owned staging path.
It does not claim that admission or target staging is implemented. The current
worker still accepts an already pinned `WorkloadReleaseIntent`.

## Regenerate the chart

From the repository root, run:

```sh
~/.agents/skills/statechart/scripts/statechart \
  docs/v3/statecharts/workload-release-admission/Release.tla \
  --config docs/v3/statecharts/workload-release-admission/Release.cfg \
  --view docs/v3/statecharts/workload-release-admission/view.ts \
  --out /tmp/norn-m5-workload-release-statechart
```

Use a new or empty output folder for each run.

## Checked behavior

The model allows a successor claim to bind an exact matching job after a lost
response. It holds when the attempt marker exists but the job is absent, or
when the job identity does not match. A delayed create may still arrive after
the controller holds, but the old workload remains active and no route change
or stop follows from that late registration.

The route sequence keeps the old workload running through old-only, mixed, and
target-only routing. The old job stops only after the controller refreshes
full target health and target-only route proof. The invariants check that the
stop has that proof, the target has an exact bound identity, traffic does not
move before binding, and the old workload remains active until stop.

The 2026-11-08 run checked 28 distinct states and 58 transitions. All listed
safety invariants and deadlock checks passed. The model omits etcd transaction
details, real Nomad and ingress behavior, credentials, workload data
compatibility, and provider failures. It is a protocol model, not a live
qualification.

The generated [statechart](chart.mmd) and [TLC report](report.md) show the
checked transition graph and result. The conservative hold after an
attempt-marker crash is deliberate. A future retry protocol needs separate
Nomad-version, ACL, retention, and delayed-request qualification.
