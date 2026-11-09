# Workload old-generation stop recovery

This bounded model isolates the durable effect around stopping the old
application deployment during M5. It requires full target health and an
observed mixed route before reserving the stop effect. It covers a stop
accepted by Nomad whose HTTP response is lost, recovery by the intent marker, allocation draining, and
completion only after every pinned old allocation is terminal.

The model was rerun with the repository's `statechart` skill on 2026-11-08;
TLC checked 10 states and 15 steps. It checks the target-health/mixed-route
prerequisite, one stop write per intent, marker uniqueness, receipt binding,
recovery without another stop write, terminal-allocation completion, and
deadlock freedom. The deterministic Go
fixture additionally closes the HTTP response after Nomad accepts the stop,
then recovers the same durable effect and verifies there was only one CAS
registration.

This is still a bounded abstraction. It does not model PostgreSQL/etcd
effect-store crashes, signature cryptography, route publication, runtime
supervision, or live Nomad behavior. The worker now dispatches signed
`app.workload-release` operations, binds them to the app lock, and joins
target-health readback, route publication, old-stop/readback, and completion
receipt. Focused tests cover each durable seam, and a driver-level lifecycle
fixture now injects lost responses after stop, drain, and other durable
boundaries before retrying the same reconciler. A production-driver
process/lease crash matrix and live qualification remain open.

Regenerate the chart and report with the repository's statechart skill:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/WorkloadStopRecovery.tla" \
  --config "$PWD/WorkloadStopRecovery.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

The output directory must not already exist.
