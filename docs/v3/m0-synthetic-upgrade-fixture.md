# Synthetic Mini control upgrade fixture

`TestSyntheticMiniControlUpgradeAndReaderBoundary` in
`v2/api/store/mini_synthetic_upgrade_integration_test.go` is the routine CI
fixture for the control-schema portion of a Mini upgrade. It creates an
unversioned legacy-shaped PostgreSQL schema, inserts wholly invented records
for device/token identity, an operation and receipt, a deployment with regional
and step state, a paused cron process, a webhook delivery, and a control event.
No row or credential was copied from Mini. The test requires a disposable
PostgreSQL database supplied as `NORN_TEST_DATABASE_URL`; the harness creates
and drops its own schema.

The test compares legacy-column reads before and after the current migration
catalog, checks that versions 1–23 apply once, verifies that a reader with
contract version 3 is refused after migration 21 raises the minimum to 4,
verifies that a writer with contract version 18 is refused after migration 22
raises the minimum to 19, verifies the current reader/writer contract, and
checks a repeat migration is a no-op. These refusals define the candidate's
binary rollback boundary; readable legacy columns alone do not permit an old
binary to resume work.

This fixture covers database shape, selected record preservation, and
compatibility metadata. It does not run the installed v2 Mini binary, Nomad,
Consul, workers, app traffic, private restore, or a full upgrade/rollback. The
private restore rehearsal remains separate because synthetic rows cannot
establish fidelity to Mini's actual distribution or size.
The [private Mini restore rehearsal](m0-mini-private-restore-migration23-rehearsal-2026-09-25.md)
reaches migration 23. The mixed-version writer boundary, guarded legacy
transition, and backup restore remain release gates.
