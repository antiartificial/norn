package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/database"
	"norn/v2/api/model"
)

func TestReviewTargetGuardRejectsRenamedReplacement(t *testing.T) {
	p, db, _ := acceptancePipelineFixture(t)
	ctx := context.Background()
	old := database.TargetIdentity{ServiceID: "pg-old", ServiceGeneration: 1, BindingID: "primary-old", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "shop", Role: "shop"}
	encoded, err := json.Marshal(recordedTargetSet{Schema: recordedTargetSetSchema, ProfileID: "mini", CatalogRevision: 1, Targets: []recordedNamedTarget{{Name: "primary", Target: old}}})
	if err != nil {
		t.Fatal(err)
	}
	op := &model.Operation{ID: uuid.NewString(), App: "rename-review", Kind: "app.deploy", Status: model.OperationSucceeded, Payload: map[string]interface{}{databaseTargetsPayloadKey: string(encoded)}}
	if err := db.InsertCompletedOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	newTarget := old
	newTarget.ServiceID, newTarget.BindingID = "pg-new", "primary-new"
	// Replacing the only logical resource is not an independent addition:
	// the old deployment can still be writing while candidates start.
	if err := p.requireRunningTargetsUnchanged(ctx, op.App, "", []recordedNamedTarget{{Name: "replacement", Target: newTarget}}); err == nil {
		t.Fatal("renaming the logical resource bypassed the database cutover guard")
	}
}

func TestReviewBaselineCannotHideKnownConflictBehindAmbiguousHistory(t *testing.T) {
	f := newNamedFixture(t)
	ctx := context.Background()
	known := database.TargetIdentity{ServiceID: "pg-a", ServiceGeneration: 1, BindingID: "shop-analytics", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "shop", Role: "shop_app"}
	encoded, err := json.Marshal(recordedTargetSet{Schema: recordedTargetSetSchema, ProfileID: "mini", CatalogRevision: 1, Targets: []recordedNamedTarget{{Name: "primary", Target: known}}})
	if err != nil {
		t.Fatal(err)
	}
	previous := &model.Operation{ID: uuid.NewString(), App: f.app, Kind: "app.deploy", Status: model.OperationSucceeded, StartedAt: time.Now().Add(-time.Hour), Payload: map[string]interface{}{databaseTargetsPayloadKey: string(encoded)}}
	if err := f.db.InsertCompletedOperation(ctx, previous); err != nil {
		t.Fatal(err)
	}
	ambiguous := &model.Operation{ID: uuid.NewString(), App: f.app, Kind: "app.deploy", Status: model.OperationFailed, StartedAt: time.Now(), Metadata: map[string]interface{}{"manualRecoveryRequired": true}}
	if err := f.db.InsertCompletedOperation(ctx, ambiguous); err != nil {
		t.Fatal(err)
	}
	// The current catalog resolves primary to pg-b. A newer unknown failure
	// does not erase the older explicit pg-a writer evidence.
	if _, err := f.queue(t, DatabaseBaselineKind, map[string]interface{}{}); err == nil {
		t.Fatal("baseline accepted conflicting target hidden behind ambiguous history")
	}
}

func TestReviewWriterFreeHistoryStillRequiresRuntimeEvidence(t *testing.T) {
	for _, status := range []model.OperationStatus{model.OperationQueued, model.OperationFailed} {
		t.Run(string(status), func(t *testing.T) {
			p, db, _ := acceptancePipelineFixture(t)
			ctx := context.Background()
			op := &model.Operation{ID: uuid.NewString(), App: "unobserved-review", Kind: "app.deploy", Status: status, Metadata: map[string]interface{}{"step": "build"}}
			if err := db.InsertOperation(ctx, op); err != nil {
				t.Fatal(err)
			}
			// No runtime client exists: neither a queued intent nor a failed
			// build proves there are no legacy jobs outside recorded history.
			next := []recordedNamedTarget{{Name: "primary", Target: database.TargetIdentity{ServiceID: "new-pg", ServiceGeneration: 1, BindingID: "new-primary", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "shop", Role: "shop"}}}
			if err := p.requireRunningTargetsUnchanged(ctx, op.App, "", next); err == nil {
				t.Fatal("writer-free history bypassed unavailable runtime evidence")
			}
		})
	}
}
