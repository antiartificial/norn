package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

// TestDeploymentStoreConformance_Postgres runs the shared deployment-store
// conformance suite against the PostgreSQL adapter. Opt-in via
// NORN_TEST_DATABASE_URL, following the repo's integration-test convention. A
// future etcd adapter gets a sibling test that calls
// runDeploymentStoreConformance with its own factory and must pass the same
// invariants.
func TestDeploymentStoreConformance_Postgres(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	runDeploymentStoreConformance(t, func(t *testing.T) DeploymentStore {
		resetOperationTables(t, db)
		return db
	})
}

// resetOperationTables clears the operation queue and the deployment-aggregate
// tables, so each conformance subtest starts empty regardless of table-wide
// operations. Children are deleted before parents to respect foreign keys.
func resetOperationTables(t *testing.T, db *DB) {
	t.Helper()
	for _, table := range []string{"deployment_steps", "deployment_regions", "deployments", "operations"} {
		if _, err := db.Pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

// runDeploymentStoreConformance is the backend-neutral behavioral contract for
// DeploymentStore. newStore must return a store backed by empty deployment
// tables on each call.
func runDeploymentStoreConformance(t *testing.T, newStore func(t *testing.T) DeploymentStore) {
	ctx := context.Background()

	newDeployment := func(app string, status model.DeployStatus) *model.Deployment {
		return &model.Deployment{
			ID:          uuid.NewString(),
			App:         app,
			Environment: "production",
			Status:      status,
			StartedAt:   time.Now().Add(-time.Minute),
		}
	}
	regions := func() []model.ResolvedRegion {
		return []model.ResolvedRegion{{Name: "nyc3", NomadRegion: "global", TrafficWeight: 100}}
	}
	mustInsert := func(t *testing.T, s DeploymentStore, d *model.Deployment) {
		t.Helper()
		if err := s.InsertDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("InsertAndGetWithRegions", func(t *testing.T) {
		s := newStore(t)
		d := newDeployment("conf-app", model.StatusQueued)
		d.SourceChanges = []string{"a.go", "b.go"}
		mustInsert(t, s, d)
		if err := s.InsertDeploymentRegions(ctx, d.ID, regions()); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDeployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.App != "conf-app" || got.Status != model.StatusQueued {
			t.Fatalf("round trip mismatch: %+v", got)
		}
		if len(got.SourceChanges) != 2 {
			t.Fatalf("source changes not preserved: %+v", got.SourceChanges)
		}
		if len(got.Regions) != 1 || got.Regions[0].Region != "nyc3" || got.Regions[0].DesiredWeight != 100 || got.Regions[0].ActiveWeight != 0 {
			t.Fatalf("regions not attached correctly: %+v", got.Regions)
		}
	})

	t.Run("InsertDeploymentRegionsIsIdempotent", func(t *testing.T) {
		s := newStore(t)
		d := newDeployment("conf-app", model.StatusQueued)
		mustInsert(t, s, d)
		if err := s.InsertDeploymentRegions(ctx, d.ID, regions()); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertDeploymentRegions(ctx, d.ID, regions()); err != nil {
			t.Fatal(err)
		}
		got, err := s.DeploymentRegions(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("duplicate region inserted: %d rows", len(got))
		}
	})

	t.Run("UpdateRegionPreservesEvalIDWhenEmpty", func(t *testing.T) {
		s := newStore(t)
		d := newDeployment("conf-app", model.StatusQueued)
		mustInsert(t, s, d)
		if err := s.InsertDeploymentRegions(ctx, d.ID, regions()); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateDeploymentRegion(ctx, d.ID, "nyc3", model.StatusSubmitting, "eval-1", "", 0); err != nil {
			t.Fatal(err)
		}
		// A later update with an empty eval must not erase the recorded one.
		if err := s.UpdateDeploymentRegion(ctx, d.ID, "nyc3", model.StatusDeployed, "", "", 100); err != nil {
			t.Fatal(err)
		}
		got, err := s.DeploymentRegions(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got[0].EvalID != "eval-1" {
			t.Fatalf("eval id not preserved on empty update: %q", got[0].EvalID)
		}
		if got[0].Status != model.StatusDeployed || got[0].ActiveWeight != 100 {
			t.Fatalf("region not updated: %+v", got[0])
		}
	})

	t.Run("FailIncompleteClosesOnlyNonTerminal", func(t *testing.T) {
		s := newStore(t)
		d := newDeployment("conf-app", model.StatusQueued)
		mustInsert(t, s, d)
		twoRegions := []model.ResolvedRegion{
			{Name: "nyc3", NomadRegion: "global", TrafficWeight: 100},
			{Name: "sfo3", NomadRegion: "global", TrafficWeight: 0},
		}
		if err := s.InsertDeploymentRegions(ctx, d.ID, twoRegions); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateDeploymentRegion(ctx, d.ID, "nyc3", model.StatusDeployed, "eval", "", 100); err != nil {
			t.Fatal(err)
		}
		if err := s.FailIncompleteDeploymentRegions(ctx, d.ID, "aborted"); err != nil {
			t.Fatal(err)
		}
		got, err := s.DeploymentRegions(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		byName := map[string]model.DeploymentRegion{}
		for _, r := range got {
			byName[r.Region] = r
		}
		if byName["nyc3"].Status != model.StatusDeployed {
			t.Fatalf("terminal region was reopened: %+v", byName["nyc3"])
		}
		if byName["sfo3"].Status != model.StatusFailed || byName["sfo3"].ActiveWeight != 0 || byName["sfo3"].LastError != "aborted" {
			t.Fatalf("incomplete region not closed: %+v", byName["sfo3"])
		}
	})

	t.Run("UpdateDeploymentSetsFinishedOnlyOnTerminal", func(t *testing.T) {
		s := newStore(t)
		d := newDeployment("conf-app", model.StatusQueued)
		mustInsert(t, s, d)
		if err := s.UpdateDeployment(ctx, d.ID, model.StatusBuilding); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDeployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.FinishedAt != nil {
			t.Fatalf("non-terminal status set finished_at: %v", got.FinishedAt)
		}
		if err := s.UpdateDeployment(ctx, d.ID, model.StatusDeployed); err != nil {
			t.Fatal(err)
		}
		got, err = s.GetDeployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.FinishedAt == nil {
			t.Fatal("terminal status did not set finished_at")
		}
	})

	t.Run("ListDeploymentsFiltersByAppAndOrders", func(t *testing.T) {
		s := newStore(t)
		older := newDeployment("app-a", model.StatusDeployed)
		older.StartedAt = time.Now().Add(-2 * time.Hour)
		newer := newDeployment("app-a", model.StatusDeployed)
		newer.StartedAt = time.Now().Add(-1 * time.Hour)
		other := newDeployment("app-b", model.StatusDeployed)
		for _, d := range []*model.Deployment{older, newer, other} {
			mustInsert(t, s, d)
		}
		got, err := s.ListDeployments(ctx, "app-a", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ID != newer.ID || got[1].ID != older.ID {
			t.Fatalf("app filter/order wrong: %+v", got)
		}
		all, err := s.ListDeployments(ctx, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 3 {
			t.Fatalf("unfiltered list = %d, want 3", len(all))
		}
	})

	t.Run("LastSuccessfulExcludesGivenID", func(t *testing.T) {
		s := newStore(t)
		first := newDeployment("app-a", model.StatusDeployed)
		first.StartedAt = time.Now().Add(-2 * time.Hour)
		second := newDeployment("app-a", model.StatusDeployed)
		second.StartedAt = time.Now().Add(-1 * time.Hour)
		mustInsert(t, s, first)
		mustInsert(t, s, second)
		got, err := s.LastSuccessfulDeployment(ctx, "app-a", second.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != first.ID {
			t.Fatalf("excluded id not honored: got %s", got.ID)
		}
	})

	t.Run("LatestSuccessfulIsPerEnvironmentAndIgnoresFailed", func(t *testing.T) {
		s := newStore(t)
		prod := newDeployment("app-a", model.StatusDeployed)
		prod.Environment = "production"
		prod.StartedAt = time.Now().Add(-2 * time.Hour)
		staging := newDeployment("app-a", model.StatusDeployed)
		staging.Environment = "staging"
		failedProd := newDeployment("app-a", model.StatusFailed)
		failedProd.Environment = "production"
		for _, d := range []*model.Deployment{prod, staging, failedProd} {
			mustInsert(t, s, d)
		}
		got, err := s.LatestSuccessfulDeployment(ctx, "app-a", "production")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != prod.ID {
			t.Fatalf("latest successful returned %s, want the deployed production row %s", got.ID, prod.ID)
		}
	})

	t.Run("RecoverInFlightFailsNonTerminalDeployments", func(t *testing.T) {
		s := newStore(t)
		d := newDeployment("app-a", model.StatusBuilding)
		mustInsert(t, s, d)
		if err := s.InsertDeploymentRegions(ctx, d.ID, regions()); err != nil {
			t.Fatal(err)
		}
		if err := s.RecoverInFlightDeployments(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDeployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.StatusFailed {
			t.Fatalf("in-flight deployment not failed: %q", got.Status)
		}
		if got.Regions[0].Status != model.StatusFailed || got.Regions[0].ActiveWeight != 0 {
			t.Fatalf("in-flight region not failed: %+v", got.Regions[0])
		}
	})

	t.Run("StepsRoundTripAndRestartResetsFinish", func(t *testing.T) {
		s := newStore(t)
		d := newDeployment("app-a", model.StatusBuilding)
		mustInsert(t, s, d)
		step := model.DeploymentStep{DeploymentID: d.ID, App: "app-a", Step: "build"}
		if err := s.StartDeploymentStep(ctx, step); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishDeploymentStep(ctx, d.ID, "build", model.DeploymentStepComplete, 1500, "built", map[string]interface{}{"image": "sha256:abc"}); err != nil {
			t.Fatal(err)
		}
		steps, err := s.ListDeploymentSteps(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(steps) != 1 || steps[0].Status != model.DeploymentStepComplete || steps[0].FinishedAt == nil || steps[0].DurationMs != 1500 {
			t.Fatalf("finished step not recorded: %+v", steps)
		}
		if steps[0].Metadata["image"] != "sha256:abc" {
			t.Fatalf("step metadata not merged: %+v", steps[0].Metadata)
		}
		// Restarting the same step (ON CONFLICT) clears the prior finish.
		if err := s.StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: d.ID, App: "app-a", Step: "build", Attempt: 2}); err != nil {
			t.Fatal(err)
		}
		steps, err = s.ListDeploymentSteps(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(steps) != 1 || steps[0].FinishedAt != nil || steps[0].Status != model.DeploymentStepRunning || steps[0].Attempt != 2 {
			t.Fatalf("restart did not reset the step: %+v", steps)
		}
	})

	t.Run("MetricsAggregateByAppAndStatus", func(t *testing.T) {
		s := newStore(t)
		for i := 0; i < 3; i++ {
			mustInsert(t, s, newDeployment("metrics-app", model.StatusDeployed))
		}
		mustInsert(t, s, newDeployment("metrics-app", model.StatusFailed))
		metrics, err := s.DeploymentMetrics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var deployed, failed int64
		for _, m := range metrics {
			if m.App != "metrics-app" {
				continue
			}
			switch m.Status {
			case model.StatusDeployed:
				deployed = m.Count
			case model.StatusFailed:
				failed = m.Count
			}
		}
		if deployed != 3 || failed != 1 {
			t.Fatalf("metrics wrong: deployed=%d failed=%d", deployed, failed)
		}
	})

	t.Run("InsertDeploymentOperationCommitsDeploymentAndRegions", func(t *testing.T) {
		s := newStore(t)
		d := newDeployment("app-a", model.StatusQueued)
		op := &model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "app-a", Status: model.OperationQueued}
		op.Payload = map[string]interface{}{"deploymentId": d.ID}
		if err := s.InsertDeploymentOperation(ctx, d, regions(), op); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDeployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.App != "app-a" || len(got.Regions) != 1 {
			t.Fatalf("composite writer did not commit deployment+regions: %+v", got)
		}
	})
}
