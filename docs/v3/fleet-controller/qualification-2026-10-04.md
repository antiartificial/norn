# Fleet controller qualification record, 2026-10-04

Base: `8b9830bc` plus the uncommitted WP14 changes. Every result below comes from a
command run in this session against local services (PostgreSQL 16 on a unix/TCP
scratch cluster, single-node etcd v3.5.17 in Docker, and the disposable three-member
TLS/RBAC etcd from `v2/scripts/test-etcd-three-member-fleet`). All runs use
`v2/scripts/go-test-strict` (`NORN_TEST_REQUIRE_INTEGRATION=1`, a skip is fatal).

## Overall status: not fully qualified. End-to-end PG bundle/restore is blocked (base-branch bug, fixed separately); section 4 rows 12 and 13 fail under strict mode (known skips / the same base bug)

One item is blocked and one is a known skip. The recovery drift is being fixed on a separate branch; `controlrecovery` registry, bundle and manifest code is untouched here.

1. **`controlrecovery` classification drift (blocks the PG authority-restore row).**
   `TestCreateVerifyRestorePassiveRoundTrip` fails identically on the unmodified base,
   because of it. An end-to-end `TestFleetAuthorityRestorePostgres` would fail at the same
   point (`CreateBundle`, before `RestorePassive` runs), so it was replaced by the narrow
   `TestFleetAuthorityEpochAdvanceAfterRestorePostgres`. The test uses an isolated, freshly migrated schema,
   so this is not test pollution. Exact diff between the migrated schema and
   `InspectionRegistry()`:
   - unknown tables (4): `external_deployment_admission_checkpoints`,
     `external_deployment_admissions`, `external_deployment_nonces`,
     `fleet_runner_checkpoint_refs`;
   - unknown columns (13): `fleet_github_dispatches.{approval_envelope_sha256,dispatch_state,pilot_run_id,rerun_started_at,run_attempt,submission_started_at}`,
     `fleet_runner_attempts.{last_error,metadata,phase_started_at,pilot_run_id,principal_subject,recovery,source_dispatch_run_id}`;
   - missing column (1): `fleet_github_dispatches.dispatch_nonce` (migration 44 removes it).
   Cause: these are the Mini extension tables and columns from
   `store/master_protected_pilot_migration.go` (commit `8ac21f8e`). `controlrecovery/registry.go`
   handles them only through `registryWithMiniExtension`, which is used by the inspection
   export (`allowMiniExtension`) but not by `CreateBundle` (`bundle.go:81`) or
   `RestorePassive`. A scratch patch routing both through the extension registry got past
   classification but then failed with "recovery manifest inventory is incomplete"
   (`manifest.go:170`), so the bundle manifest also lacks these tables. That patch was reverted.
   The human decided (2026-10-04) that this is fixed on a separate branch; until it lands,
   only the narrow PG test below covers the restore epoch step.
2. `TestEtcdFleetStagingReleaseHTTPToDisposableNomad` skips (needs Nomad), so
   `go-test-strict` fails the root-package line (see exclusions).

## WP14 changes

- `controlrecovery/restore.go`: the read-only validation transaction also reads the restored
  epoch; after it commits, `RestorePassive` CASes the restored `fleet_authority_epoch` from that
  value to value+1 (reason `restore:<bundleId>`) and returns it in
  `RestoreReport.FleetAuthorityEpoch`. It rewrites no fence, attempt or history row. If the
  advance fails, the error is returned with no report, the target stays passive, and a retry is
  refused by the target-emptiness check (no double advance).
  `RestorePassive` itself (bundle through restore) is not exercised end to end because of blocker 1.
- `etcdstore/fleet_authority_restore_integration_test.go`: `TestFleetAuthorityRestoreEtcd/{seed,verify}`.
- `controlrecovery/fleet_epoch_restore_integration_test.go`: `TestFleetAuthorityEpochAdvanceAfterRestorePostgres`.
  It calls the extracted unexported `advanceRestoredFleetAuthorityEpoch` (the step `RestorePassive`
  runs after verification) on an isolated migrated schema with seeded fence, attempt and dispatch
  state. It asserts: epoch advanced by exactly one with reason `restore:<bundle>`; a repeated
  advance from the restored epoch is refused (CAS); no row added or removed and no
  fence rewritten; held fence derives Uncertain/AuthoritySuperseded; old-epoch heartbeat, advance
  and succeeded checkpoint refused through the real PG store paths; failed checkpoint and cancel
  still allowed (Q10); first attempt rebinds under the new epoch with Generation+1 (M13).
- `v2/scripts/test-etcd-three-member-fleet`: stages exactly as WP14 lists, plus the strict env.
- Plumbing fix needed to run the race test under TLS: `internal/integrationtest.EtcdClient`
  (new, `Etcd` now uses it) and `TestFleetFenceTwoAdapterAcquireRaceEtcd` uses it for its two
  adapters. The test dialed plain endpoints, so it hung against the TLS/RBAC cluster
  (first script run, killed after ~8 minutes). No assertion changed.

## Section 4 full run

| # | Command | Backend | Result |
|---|---|---|---|
| 1 | `gofmt -l .` | n/a | lists `pipeline/acceptance_integration_test.go`, which is also listed on the pristine base and untouched here (pre-existing); nothing from WP14 |
| 2 | `go vet ./fleet/... ./store/... ./etcdstore/... ./handler/... ./githubapp/...` | n/a | PASS |
| 3 | `go build -buildvcs=false ./...` | n/a | PASS |
| 4 | `$S ./fleet/... '.' -race` | n/a | PASS |
| 5 | `$S ./githubapp 'Observe\|ListPlanRuns' -race` | n/a | PASS |
| 6 | `$S ./store '^TestFleet(Target\|Resource\|Reconciler)ConformancePostgres$' -race` | PG | PASS |
| 7 | `$S ./etcdstore '^TestFleet(Target\|Resource\|Reconciler)ConformanceEtcd$\|TestFleetFenceTwoAdapterAcquireRaceEtcd'` | single-node etcd | PASS (re-run after the integrationtest change) |
| 8 | `$S ./handler '^TestFleet(Lifecycle\|Fence)Conformance(Postgres\|Etcd)$'` | PG + single-node etcd | PASS (re-run after the change) |
| 9 | `$S ./store 'Fleet\|SchemaMigration' -race` | PG | PASS |
| 10 | `$S ./etcdstore '^TestV3Fleet'` | single-node etcd | PASS (re-run) |
| 11 | `$S ./handler 'Fleet'` | PG + etcd | PASS (re-run after the integrationtest change) |
| 12 | `$S . 'EtcdFleet\|FleetAuthorityOnly\|PGFree\|FleetTarget\|FleetResource'` | PG + etcd | FAIL by strictness only: package `ok`, but `TestEtcdFleetStagingReleaseHTTPToDisposableNomad` and `TestEtcdFleetRuntimeProductionTLSRBACProcess` skip (known exclusions) |
| 13 | `$S ./controlrecovery '.'` | PG | FAIL only on `TestCreateVerifyRestorePassiveRoundTrip`, which fails on the pristine base for the same drift (blocker 1); `TestFleetAuthorityEpochAdvanceAfterRestorePostgres` PASS (re-run alone after the review's CAS change: PASS) |
| 14 | `$S ./retention '.'` | PG | PASS |
| 15 | `v2/scripts/test-etcd-three-member-fleet` | three-member TLS/RBAC etcd | PASS, final line below |

Final line of row 15: `PASS: disposable three-member TLS/RBAC, isolated-member partition and rejoin, one loss, quorum refusal, corruption rejection, snapshot restore, Fleet suites and authority restore`.
The quorum-loss stage refused both the linearizable read and the write (the script exits 1 otherwise).
`TestEtcdFleetRuntimeProductionTLSRBACProcess` ran and passed in four stages of the script (normal, partition, one-member loss, post-restore), so its skip in row 12 is covered there.

## Qualification matrix

| Matrix item | Tests run | Backend | Result |
|---|---|---|---|
| Two controllers / old controller resumes | `RunFleetResourceConformance/ConcurrentReconcileWritesConverge`, `/OldEpochWriterRecomputes`, `/ReconcileWriteSeesLatestInputs`; `RunFleetReconcilerConformance/TwoReconcilersConverge`, via `TestFleet{Resource,Reconciler}Conformance{Postgres,Etcd}` (rows 6, 7, three-member healthy stage) | PG, single-node etcd, three-member TLS | PASS |
| Two plans incl. aliases | `RunFleetFenceConformance/SecondPlanBlockedWhileHeld`, `/AliasDoesNotBypass`, `/UnregisteredClusterRefusedWhenRegistryNonEmpty`, `/RegisterWhileInFlightRefused` via `TestFleetFenceConformance{Postgres,Etcd}` (row 8; etcd also row 15); `TestFleetTargetConformance{Postgres,Etcd}` (rows 6, 7); `TestFleetFenceTwoAdapterAcquireRaceEtcd` (row 7; partition stage of row 15) | PG, single-node etcd, three-member TLS with one member partitioned | PASS |
| Provider mutation succeeds, checkpoint lost | `RunFleetFenceConformance/ExpiryDoesNotRelease` via `TestFleetFenceConformance{Postgres,Etcd}` (row 8, row 15); `TestDeriveInterruptedExecutionIsUncertain` (row 4, `./fleet/...`) | PG, etcd | PASS |
| Checkpoint succeeds, executor crashes | `RunFleetLifecycleConformance/CheckpointReplayAfterLostResponse` via `TestFleetLifecycleConformance{Postgres,Etcd}` (row 8; etcd also in row 15) | live PG legacy, etcd | PASS |
| Dispatch ambiguous | `RunFleetFenceConformance/DispatchAmbiguousKeepsOccupiedThenAbandonResolves`, `/AbandonedPlanCannotStartRecoverOrFinish` via `TestFleetFenceConformance{Postgres,Etcd}` (row 8); `TestDispatchAmbiguousOrdinaryLaneOccupiesTarget` (run separately with `$S ./handler '^TestDispatchAmbiguousOrdinaryLaneOccupiesTarget$'`, PASS; the section 4 `'Fleet'` regex does not match its name); `TestDeriveDispatchAmbiguous` (row 4) | PG, etcd | PASS |
| Desired changes during execution | `RunFleetResourceConformance/DesiredChangeDuringExecutionKeepsBindings` via `TestFleetResourceConformance{Postgres,Etcd}` (rows 6, 7); `TestDeriveDesiredChangedDuringExecution` (row 4) | PG, etcd | PASS |
| Old observations after newer failures | `RunFleetResourceConformance/OlderObservationStoredNotApplied`, `/PruneNeverResurrectsOlderObservation` via `TestFleetResourceConformance{Postgres,Etcd}` (rows 6, 7); `TestDeriveOlderObservationCannotClearFailure` (row 4) | PG, etcd | PASS |
| Authority restore, PG | `TestFleetAuthorityEpochAdvanceAfterRestorePostgres` (row 13) | PG, isolated migrated schema, extracted restore step | narrow test PASS; end-to-end bundle/restore **blocked** by the base-branch recovery drift (4 tables and 13+1 columns listed in blocker 1) |
| Authority restore, etcd | `TestFleetAuthorityRestoreEtcd/seed` (healthy stage), `/verify` (after real `etcdutl snapshot restore`) (row 15); both phases in one run on single-node etcd | three-member TLS, restored from snapshot | PASS (row 15 re-run after the review tightened `/verify` to require exactly `*lifecycle.FenceError` and an unchanged attempt after the refused heartbeat; exit 0 with the final PASS line below) |
| Authority epoch semantics | `RunFleetFenceConformance/EpochAdvanceRefusesHeartbeatAdvanceSuccessCheckpoint`, `/FailedCheckpointAndCancelAllowedAfterEpochAdvance`, `/EpochAdvanceBeforeFirstAttemptRebinds` inside `TestFleetFenceConformance{Postgres,Etcd}` (row 8) | PG, etcd | PASS |
| PG and three-member etcd | every `TestFleet*Conformance{Postgres,Etcd}` on PG and single-node etcd (rows 6-8); `TestFleet(Target\|Resource\|Reconciler)ConformanceEtcd`, `TestFleet(Lifecycle\|Fence)ConformanceEtcd` on the three-member TLS cluster (row 15) | PG, single-node, three-member TLS | PASS, skips fatal |

Subtest names above were checked against the `t.Run` names in `fleet/fleettest/{fence,lifecycle,resource,reconciler}.go`,
and top-level names with `go test -list`. Subtests run under their parent tests; the parent PASS covers them.

## Known exclusions

- `TestEtcdFleetStagingReleaseHTTPToDisposableNomad`: needs a disposable Nomad, not available. Pre-existing skip.
- `TestEtcdFleetRuntimeProductionTLSRBACProcess` in the root package: needs TLS etcd, so it skips against single-node etcd. It is run and passes inside the three-member script.
- End-to-end PG bundle/restore (including `TestCreateVerifyRestorePassiveRoundTrip`, which fails on the base): blocked by blocker 1.
- Darwin sampler handler test (`docs/v3/implementation-status.md`): not matched by the `'Fleet'` regex; `go test ./handler -list Fleet` lists no sampler test.
- `gofmt -l` entry `pipeline/acceptance_integration_test.go`: pre-existing, outside this change.
- Production etcd restore wiring: deferred (H5); the etcd side is the script stage only.
