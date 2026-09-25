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
catalog, checks that versions 1–18 apply once, verifies the prior reader
contract is refused after migration 17 raises the minimum to 3, verifies the
current reader/writer contract (including migration 18's writer minimum 15),
and checks a repeat migration is a no-op.
The refusal is intentional: a binary rollback to a reader below version 3 is
outside the supported window after migration 17.

This fixture covers database shape, selected record preservation, and
compatibility metadata. It does not run the installed v2 Mini binary, Nomad,
Consul, workers, app traffic, private restore, or a full upgrade/rollback. The
private restore rehearsal remains separate because synthetic rows cannot
establish fidelity to Mini's actual distribution or size.
The private Mini restore rehearsal currently stops at migration 17; migration
18 and the mixed-version writer boundary remain a release gate.
