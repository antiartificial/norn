package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// fleetTargetConformanceDB gives this suite its own schema on
// NORN_TEST_DATABASE_URL, migrated and torn down like
// internal/integrationtest.PG. It cannot use that package directly: store is
// upstream of internal/integrationtest (which returns *store.DB), so
// importing it from a store package test file is an import cycle. Its
// NORN_TEST_REQUIRE_INTEGRATION fail-fatal behavior (WP1's "Conventions")
// is preserved here instead.
func fleetTargetConformanceDB(t *testing.T) *DB {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("NORN_TEST_DATABASE_URL"))
	if databaseURL == "" {
		if os.Getenv("NORN_TEST_REQUIRE_INTEGRATION") != "" {
			t.Fatal("NORN_TEST_DATABASE_URL is not set")
		}
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schemaName := "fleet_target_conformance_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") })
	testConfig := adminConfig.Copy()
	if testConfig.ConnConfig.RuntimeParams == nil {
		testConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	testConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	if testConfig.MaxConns < 4 {
		testConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, testConfig)
	if err != nil {
		t.Fatal(err)
	}
	db := &DB{Pool: pool}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// fleetTargetTestHarness implements fleettest.TargetHarness against a real
// PG control schema.
type fleetTargetTestHarness struct {
	db *DB
}

func (h fleetTargetTestHarness) Register(ctx context.Context, identity lifecycle.TargetIdentity, aliases []string, operationID string) (string, error) {
	target, err := h.db.RegisterFleetTarget(ctx, identity, aliases, operationID)
	if err != nil {
		return "", err
	}
	return target.TargetID, nil
}

func (h fleetTargetTestHarness) IsAliasConflict(err error) bool {
	var fe *lifecycle.FenceError
	return errors.As(err, &fe) && fe.Code == lifecycle.CodeFleetTargetAliasConflict
}

func (h fleetTargetTestHarness) IsRegistrationInFlight(err error) bool {
	var fe *lifecycle.FenceError
	return errors.As(err, &fe) && fe.Code == lifecycle.CodeFleetTargetRegistrationInFlight
}

func (h fleetTargetTestHarness) RegistryGeneration(ctx context.Context) (int64, error) {
	var generation int64
	err := h.db.Pool.QueryRow(ctx, `SELECT generation FROM fleet_target_registry WHERE singleton`).Scan(&generation)
	return generation, err
}

func (h fleetTargetTestHarness) SeedInFlightDispatch(ctx context.Context, cluster, environment, dispatchState string) (string, error) {
	now := time.Now().UTC()
	planID := uuid.NewString()
	plan := &model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
		Payload: map[string]interface{}{"cluster": cluster}, StartedAt: now, UpdatedAt: now, FinishedAt: &now,
	}
	if err := h.db.InsertCompletedOperation(ctx, plan); err != nil {
		return "", err
	}
	item := FleetGitHubDispatch{
		PlanID: planID, PlanRunID: 1, PlanSHA256: "sha-" + planID, ApprovedHeadSHA: "head-" + planID,
		FleetEnvironment: environment, DispatchNonceSHA256: "nonce-" + planID, DispatchState: dispatchState,
	}
	if _, err := h.db.CreateFleetGitHubDispatch(ctx, item); err != nil {
		return "", err
	}
	return planID, nil
}

func (h fleetTargetTestHarness) Epoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := h.db.Pool.QueryRow(ctx, `SELECT epoch FROM fleet_authority_epoch WHERE singleton`).Scan(&epoch)
	return epoch, err
}

func (h fleetTargetTestHarness) AdvanceEpoch(ctx context.Context, expected int64, reason string) (int64, error) {
	return h.db.AdvanceFleetAuthorityEpoch(ctx, expected, reason)
}

func (h fleetTargetTestHarness) IsEpochConflict(err error) bool {
	return errors.Is(err, ErrFleetAuthorityEpochConflict)
}

func (h fleetTargetTestHarness) SeedFence(ctx context.Context, targetID string, authorityEpoch int64) error {
	_, err := h.db.Pool.Exec(ctx, `
		INSERT INTO fleet_target_fences (target_id, generation, held, holder_plan_id, holder_nonce_sha256, authority_epoch, revision)
		VALUES ($1, 1, true, 'seed-plan', 'seed-nonce', $2, 1)
		ON CONFLICT (target_id) DO UPDATE SET generation = EXCLUDED.generation, held = EXCLUDED.held,
			holder_plan_id = EXCLUDED.holder_plan_id, holder_nonce_sha256 = EXCLUDED.holder_nonce_sha256,
			authority_epoch = EXCLUDED.authority_epoch, revision = EXCLUDED.revision
	`, targetID, authorityEpoch)
	return err
}

func (h fleetTargetTestHarness) FenceAuthorityEpoch(ctx context.Context, targetID string) (int64, error) {
	var epoch int64
	err := h.db.Pool.QueryRow(ctx, `SELECT authority_epoch FROM fleet_target_fences WHERE target_id=$1`, targetID).Scan(&epoch)
	return epoch, err
}

func TestFleetTargetConformancePostgres(t *testing.T) {
	db := fleetTargetConformanceDB(t)
	fleettest.RunFleetTargetConformance(t, fleetTargetTestHarness{db: db})
}

// TestFleetTargetSchemaMigrationRaisesWriterFloor confirms migration 48's H1
// writer floor: once it is applied, a writer declaring the previous control
// schema writer version (31) is rejected, and the current writer version
// (32) is accepted, independent of whether any Fleet target is registered.
// Migration 49 (WP10's fleet_resources/fleet_observations) keeps the same
// floor, so it is now the last migration this checks against.
func TestFleetTargetSchemaMigrationRaisesWriterFloor(t *testing.T) {
	db := fleetTargetConformanceDB(t)
	ctx := context.Background()
	migrations := ControlSchemaMigrations()
	last := migrations[len(migrations)-1]
	if last.Version != 49 || last.MinimumWriterVersion != FleetTargetFenceWriterVersion || FleetTargetFenceWriterVersion != 32 {
		t.Fatalf("migration 49 contract = %+v, FleetTargetFenceWriterVersion=%d", last, FleetTargetFenceWriterVersion)
	}
	if ControlSchemaWriterVersion != FleetTargetFenceWriterVersion {
		t.Fatalf("ControlSchemaWriterVersion = %d, want %d", ControlSchemaWriterVersion, FleetTargetFenceWriterVersion)
	}
	oldWriter, err := NewSchemaMigrator(db.Pool, migrations, BinarySchemaCompatibility{ReaderVersion: ControlSchemaReaderVersion, WriterVersion: 31}, SchemaMigratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = oldWriter.Check(ctx, SchemaAccessReadWrite)
	var compatibilityErr *SchemaCompatibilityError
	if !errors.As(err, &compatibilityErr) || compatibilityErr.Required != FleetTargetFenceWriterVersion || compatibilityErr.Provided != 31 {
		t.Fatalf("a writer below the new floor must be rejected, got %T %v", err, err)
	}
	currentWriter, err := NewSchemaMigrator(db.Pool, migrations, BinarySchemaCompatibility{ReaderVersion: ControlSchemaReaderVersion, WriterVersion: FleetTargetFenceWriterVersion}, SchemaMigratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := currentWriter.Check(ctx, SchemaAccessReadWrite); err != nil {
		t.Fatalf("a writer at the new floor must be accepted, got %v", err)
	}
}

// acquireFleetTargetFenceForTest drives the WP3 primitives in plan.md §2.2's
// lock order (registry FOR SHARE, epoch FOR SHARE, fence FOR UPDATE) and
// writes DecideAcquire's result, standing in for WP8a's fenced submit. hold
// keeps the transaction open after the fence write to widen the race window.
func acquireFleetTargetFenceForTest(ctx context.Context, db *DB, cluster, environment, planID string, hold time.Duration) error {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	targetID, empty, err := ResolveFleetTargetForPlan(ctx, tx, cluster, environment)
	if err != nil {
		return err
	}
	if empty {
		return errors.New("registry unexpectedly empty")
	}
	epoch, err := FleetAuthorityEpoch(ctx, tx)
	if err != nil {
		return err
	}
	fence, err := GetFleetTargetFence(ctx, tx, targetID, true)
	if err != nil {
		return err
	}
	next, err := lifecycle.DecideAcquire(fence, planID, "nonce-"+planID, time.Now(), epoch)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE fleet_target_fences SET generation=$2, held=$3, holder_plan_id=$4, holder_nonce_sha256=$5, authority_epoch=$6, revision=$7 WHERE target_id=$1`,
		targetID, next.Generation, next.Held, next.HolderPlanID, next.HolderNonceSHA256, next.AuthorityEpoch, next.Revision); err != nil {
		return err
	}
	time.Sleep(hold)
	return tx.Commit(ctx)
}

// TestFleetTargetFenceConcurrencyPostgres races real PG transactions on
// separate connections under READ COMMITTED (M1, plan.md §2.2).
func TestFleetTargetFenceConcurrencyPostgres(t *testing.T) {
	db := fleetTargetConformanceDB(t)
	ctx := context.Background()
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "555555555555", StateBackend: "s3://race-bucket/state"}
	target, err := db.RegisterFleetTarget(ctx, identity, []string{"cluster:race", "environment:race-prod"}, "op-race")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("AcquireRaceThroughBothAliasesHasOneWinner", func(t *testing.T) {
		const racers = 8
		start := make(chan struct{})
		errs := make(chan error, racers)
		for i := 0; i < racers; i++ {
			environment := ""
			if i%2 == 1 {
				environment = "race-prod" // resolves through the environment alias too
			}
			planID := uuid.NewString()
			go func() {
				<-start
				errs <- acquireFleetTargetFenceForTest(ctx, db, "race", environment, planID, 50*time.Millisecond)
			}()
		}
		close(start)
		winners := 0
		for i := 0; i < racers; i++ {
			err := <-errs
			var fe *lifecycle.FenceError
			switch {
			case err == nil:
				winners++
			case errors.As(err, &fe) && fe.Code == lifecycle.CodeFleetTargetExecutionOccupied:
			default:
				t.Fatalf("racer failed with %v, want nil or fleet_target_execution_occupied", err)
			}
		}
		if winners != 1 {
			t.Fatalf("exactly one concurrent acquire must win, got %d", winners)
		}
		var generation int64
		if err := db.Pool.QueryRow(ctx, `SELECT generation FROM fleet_target_fences WHERE target_id=$1 AND held`, target.TargetID).Scan(&generation); err != nil || generation != 1 {
			t.Fatalf("the winning fence must be held at generation 1, got %d, %v", generation, err)
		}
	})

	t.Run("RegistrationWaitsForUnfencedAdmissionThenRefuses", func(t *testing.T) {
		// A fresh database, so the admission sees an empty registry and
		// proceeds unfenced while holding the registry FOR SHARE.
		db := fleetTargetConformanceDB(t)
		now := time.Now().UTC()
		planID := uuid.NewString()
		if err := db.InsertCompletedOperation(ctx, &model.Operation{ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
			Payload: map[string]interface{}{"cluster": "late"}, StartedAt: now, UpdatedAt: now, FinishedAt: &now}); err != nil {
			t.Fatal(err)
		}
		admission, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = admission.Rollback(ctx) }()
		if _, empty, err := ResolveFleetTargetForPlan(ctx, admission, "late", ""); err != nil || !empty {
			t.Fatalf("expected an empty registry, got empty=%v err=%v", empty, err)
		}
		if _, err := admission.Exec(ctx, `INSERT INTO fleet_github_dispatches (plan_id, plan_run_id, plan_sha256, approved_head_sha, fleet_environment, allow_destructive, dispatch_nonce_sha256, dispatch_state, submission_started_at)
			VALUES ($1, 1, 'sha', 'head', '', false, 'nonce', 'submitting', now())`, planID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := db.RegisterFleetTarget(ctx, lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "666666666666", StateBackend: "s3://late/state"}, []string{"cluster:late"}, "op-late")
			done <- err
		}()
		select {
		case err := <-done:
			t.Fatalf("registration must block on the admission's registry FOR SHARE, returned %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := admission.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var fe *lifecycle.FenceError
		if err := <-done; !errors.As(err, &fe) || fe.Code != lifecycle.CodeFleetTargetRegistrationInFlight {
			t.Fatalf("registration after a committed unfenced admission must refuse as in flight, got %v", err)
		}
	})

	t.Run("AdmissionWaitsForRegistrationThenResolves", func(t *testing.T) {
		db := fleetTargetConformanceDB(t)
		registration, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = registration.Rollback(ctx) }()
		registered, err := registerFleetTargetTx(ctx, registration, lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "777777777777", StateBackend: "s3://early/state"}, []string{"cluster:early"}, "op-early", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		type result struct {
			targetID string
			empty    bool
			err      error
		}
		done := make(chan result, 1)
		go func() {
			tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				done <- result{err: err}
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			targetID, empty, err := ResolveFleetTargetForPlan(ctx, tx, "early", "")
			done <- result{targetID, empty, err}
		}()
		select {
		case r := <-done:
			t.Fatalf("admission must block on the registration's registry FOR UPDATE, returned %+v", r)
		case <-time.After(300 * time.Millisecond):
		}
		if err := registration.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if r := <-done; r.err != nil || r.empty || r.targetID != registered.TargetID {
			t.Fatalf("admission after registration commits must resolve the new target, got %+v", r)
		}
	})
}
