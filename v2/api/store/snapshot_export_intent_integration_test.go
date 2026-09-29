package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
)

func TestSnapshotExportIntentSurvivesClaimTurnover(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_snapshot_export_intent")
	db, err := Connect(server.URL("norn_snapshot_export_intent"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	op := insertOperationFixture(t, db, "app.snapshot-export", 2, map[string]interface{}{})
	claimed, first, err := db.ClaimNextOperation(ctx, "first-export-worker", time.Minute, []string{"app.snapshot-export"})
	if err != nil || claimed == nil {
		t.Fatalf("first claim = %+v, %v", claimed, err)
	}
	want := SnapshotExportIntent{OperationID: op.ID, Bucket: "retained", ObjectKey: "snapshots/demo/operations/" + op.ID + "/dump",
		DumpSHA256: strings.Repeat("a", 64), DumpSize: 17, ManifestSHA256: strings.Repeat("b", 64)}
	if err := db.PrepareSnapshotExportIntent(ctx, first, want); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, op.ID, want.ObjectKey).Scan(&state); err != nil || state != "prepared" {
		t.Fatalf("remote-write reservation = %q, %v", state, err)
	}
	changed := want
	changed.DumpSHA256 = strings.Repeat("c", 64)
	if err := db.PrepareSnapshotExportIntent(ctx, first, changed); err == nil {
		t.Fatal("changed bytes reused the existing export reservation")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='queued', locked_by='', locked_until=NULL, next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	claimed, second, err := db.ClaimNextOperation(ctx, "second-export-worker", time.Minute, []string{"app.snapshot-export"})
	if err != nil || claimed == nil || second.Generation() == first.Generation() {
		t.Fatalf("successor claim = %+v, %+v, %v", claimed, second, err)
	}
	if err := db.PrepareSnapshotExportIntent(ctx, first, want); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale claimant reservation = %v", err)
	}
	if err := db.PrepareSnapshotExportIntent(ctx, second, want); err != nil {
		t.Fatalf("successor could not adopt exact export: %v", err)
	}
	if err := db.RecordSnapshotExportReceipt(ctx, changed); err == nil {
		t.Fatal("changed bytes recorded a publication receipt")
	}
	if err := db.RecordSnapshotExportReceipt(ctx, want); err != nil {
		t.Fatalf("verified publication receipt: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT state FROM snapshot_export_intents WHERE operation_id=$1 AND object_key=$2`, op.ID, want.ObjectKey).Scan(&state); err != nil || state != "published" {
		t.Fatalf("completed export = %q, %v", state, err)
	}
}

func TestSnapshotExportRecoveryReclaimsOnlyDurableIntent(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_snapshot_export_recovery")
	db, err := Connect(server.URL("norn_snapshot_export_recovery"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	withIntent := insertOperationFixture(t, db, "app.snapshot-export", 1, map[string]interface{}{})
	claimed, first, err := db.ClaimNextOperation(ctx, "first-export-worker", time.Minute, []string{"app.snapshot-export"})
	if err != nil || claimed == nil || claimed.ID != withIntent.ID {
		t.Fatalf("first claim = %+v, %v", claimed, err)
	}
	want := SnapshotExportIntent{OperationID: withIntent.ID, Bucket: "retained", ObjectKey: "snapshots/demo/operations/" + withIntent.ID + "/dump",
		DumpSHA256: strings.Repeat("a", 64), DumpSize: 17, ManifestSHA256: strings.Repeat("b", 64)}
	if err := db.PrepareSnapshotExportIntent(ctx, first, want); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, withIntent.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.GetOperation(ctx, withIntent.ID)
	if err != nil || recovered.Status != "queued" || recovered.MaxAttempts != 3 {
		t.Fatalf("durably reserved export recovery = %+v, %v", recovered, err)
	}
	claimed, second, err := db.ClaimNextOperation(ctx, "second-export-worker", time.Minute, []string{"app.snapshot-export"})
	if err != nil || claimed == nil || claimed.ID != withIntent.ID || second.Generation() == first.Generation() {
		t.Fatalf("successor claim = %+v, %+v, %v", claimed, second, err)
	}
	if err := db.PrepareSnapshotExportIntent(ctx, second, want); err != nil {
		t.Fatalf("successor could not adopt exact intent: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='failed',locked_by='',locked_until=NULL,finished_at=now() WHERE id=$1`, withIntent.ID); err != nil {
		t.Fatal(err)
	}
	withoutIntent := insertOperationFixture(t, db, "app.snapshot-export", 1, map[string]interface{}{})
	claimed, _, err = db.ClaimNextOperation(ctx, "unreserved-export-worker", time.Minute, []string{"app.snapshot-export"})
	if err != nil || claimed == nil || claimed.ID != withoutIntent.ID {
		t.Fatalf("unreserved claim = %+v, %v", claimed, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, withoutIntent.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	unreserved, err := db.GetOperation(ctx, withoutIntent.ID)
	if err != nil || unreserved.Status != "failed" {
		t.Fatalf("unreserved export recovered automatically: %+v, %v", unreserved, err)
	}
}

func TestDeploySnapshotExportRecoveryStopsBeforeOtherMutableSteps(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_deploy_export_recovery")
	db, err := Connect(server.URL("norn_deploy_export_recovery"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pinnedImage := "registry.example/demo@sha256:" + strings.Repeat("a", 64)
	makeExpired := func(worker string, withIntent, laterMutable bool, imageRef string) (*model.Deployment, *model.Operation) {
		t.Helper()
		deployment, op := insertDeploymentOperationFixture(t, db, 2)
		claimed, claim, err := db.ClaimNextOperation(ctx, worker, time.Minute, []string{"app.deploy"})
		if err != nil || claimed == nil || claimed.ID != op.ID {
			t.Fatalf("deploy claim = %+v, %v", claimed, err)
		}
		if err := db.StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: deployment.ID, App: deployment.App,
			SagaID: deployment.SagaID, Step: "snapshot", Kind: model.DeploymentStepMutable, Status: model.DeploymentStepRunning, Attempt: 1}); err != nil {
			t.Fatal(err)
		}
		if withIntent {
			want := SnapshotExportIntent{OperationID: op.ID, Bucket: "retained", ObjectKey: "snapshots/" + deployment.App + "/operations/" + op.ID + "/dump",
				DumpSHA256: strings.Repeat("a", 64), DumpSize: 17, ManifestSHA256: strings.Repeat("b", 64)}
			if err := db.PrepareSnapshotExportIntent(ctx, claim, want); err != nil {
				t.Fatal(err)
			}
		}
		if imageRef != "" {
			for _, stage := range []string{CheckpointSource, CheckpointBuild} {
				payload := json.RawMessage(`{"pinned":true}`)
				if stage == CheckpointBuild {
					payload, err = json.Marshal(map[string]string{"imageTag": imageRef})
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.RecordOperationCheckpoint(ctx, claim, stage, payload); err != nil {
					t.Fatal(err)
				}
			}
		}
		if laterMutable {
			if err := db.StartDeploymentStep(ctx, model.DeploymentStep{DeploymentID: deployment.ID, App: deployment.App,
				SagaID: deployment.SagaID, Step: "migrate", Kind: model.DeploymentStepMutable, Status: model.DeploymentStepRunning, Attempt: 1}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID); err != nil {
			t.Fatal(err)
		}
		return deployment, op
	}
	_, safe := makeExpired("snapshot-export-worker", true, false, pinnedImage)
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.GetOperation(ctx, safe.ID)
	if err != nil || recovered.Status != model.OperationQueued || recovered.Attempts != 1 || recovered.MaxAttempts != 2 {
		t.Fatalf("snapshot-only deploy recovery = %+v, %v", recovered, err)
	}
	claimed, second, err := db.ClaimNextOperation(ctx, "snapshot-export-successor", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != safe.ID || second.Generation() < 2 {
		t.Fatalf("snapshot-only successor = %+v, %+v, %v", claimed, second, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='failed',locked_by='',locked_until=NULL,finished_at=now() WHERE id=$1`, safe.ID); err != nil {
		t.Fatal(err)
	}
	_, advanced := makeExpired("advanced-deploy-worker", true, true, pinnedImage)
	_, unreserved := makeExpired("unreserved-deploy-worker", false, false, pinnedImage)
	_, uncheckpointed := makeExpired("uncheckpointed-deploy-worker", true, false, "")
	_, mutableImage := makeExpired("mutable-image-deploy-worker", true, false, "demo:latest")
	_, corruptCheckpoint := makeExpired("corrupt-checkpoint-deploy-worker", true, false, pinnedImage)
	if _, err := db.Pool.Exec(ctx, `UPDATE operation_checkpoints SET outputs=decode('fffe','hex') WHERE operation_id=$1 AND stage='build'`, corruptCheckpoint.ID); err != nil {
		t.Fatal(err)
	}
	_, otherSafe := makeExpired("other-safe-deploy-worker", true, false, pinnedImage)
	if err := db.RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	for _, op := range []*model.Operation{advanced, unreserved, uncheckpointed, mutableImage, corruptCheckpoint} {
		got, err := db.GetOperation(ctx, op.ID)
		if err != nil || got.Status != model.OperationFailed {
			t.Fatalf("unsafe deploy recovery = %+v, %v", got, err)
		}
	}
	if got, err := db.GetOperation(ctx, otherSafe.ID); err != nil || got.Status != model.OperationQueued {
		t.Fatalf("corrupt checkpoint blocked another safe deploy's recovery: %+v, %v", got, err)
	}
}
