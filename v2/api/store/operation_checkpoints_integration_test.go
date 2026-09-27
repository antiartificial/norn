package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestOperationCheckpointIsClaimFencedWriteOnceAndVerified(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	db := dbs[0]
	ctx := context.Background()
	if migration := operationCheckpointsMigration(); migration.Version != 4 || migration.MinimumWriterVersion != 4 || ControlSchemaWriterVersion < 4 {
		t.Fatalf("checkpoint migration contract = %+v writer=%d", migration, ControlSchemaWriterVersion)
	}
	op, claim := claimEffectOperation(t, db, "worker-a", time.Minute)
	if checkpoint, err := db.LoadOperationCheckpoint(ctx, op.ID, CheckpointSource); err != nil || checkpoint != nil {
		t.Fatalf("empty checkpoint = %+v, %v", checkpoint, err)
	}
	first := json.RawMessage(`{"commitSha":"abc","treeDigest":"sha256:1"}`)
	stored, err := db.RecordOperationCheckpoint(ctx, claim, CheckpointSource, first)
	if err != nil || string(stored.Outputs) != string(first) || stored.ClaimGeneration != claim.Generation() {
		t.Fatalf("record = %+v, %v", stored, err)
	}
	if again, err := db.RecordOperationCheckpoint(ctx, claim, CheckpointSource, first); err != nil || again.OutputsDigest != stored.OutputsDigest {
		t.Fatalf("identical re-record = %+v, %v", again, err)
	}
	conflict, err := db.RecordOperationCheckpoint(ctx, claim, CheckpointSource, json.RawMessage(`{"commitSha":"def"}`))
	if !errors.Is(err, ErrCheckpointConflict) || string(conflict.Outputs) != string(first) {
		t.Fatalf("conflicting record = %+v, %v", conflict, err)
	}
	for name, outputs := range map[string]json.RawMessage{"not json": json.RawMessage(`{`), "empty": nil} {
		if _, err := db.RecordOperationCheckpoint(ctx, claim, CheckpointBuild, outputs); err == nil {
			t.Fatalf("%s outputs accepted", name)
		}
	}
	if _, err := db.RecordOperationCheckpoint(ctx, claim, "deploy", first); err == nil {
		t.Fatal("unknown stage accepted")
	}

	// A claim that is no longer the live owner cannot record.
	stale, err := NewOperationClaim(claim.OperationID(), claim.OwnerID(), claim.Generation()+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordOperationCheckpoint(ctx, stale, CheckpointBuild, json.RawMessage(`{"image":"x"}`)); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale claim record = %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET locked_until = now() - interval '1 second' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordOperationCheckpoint(ctx, claim, CheckpointBuild, json.RawMessage(`{"image":"x"}`)); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("expired claim record = %v", err)
	}

	// Stored bytes are verified against their digest on every read.
	if _, err := db.Pool.Exec(ctx, `UPDATE operation_checkpoints SET outputs = convert_to('{"commitSha":"forged"}','UTF8') WHERE operation_id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadOperationCheckpoint(ctx, op.ID, CheckpointSource); err == nil {
		t.Fatal("tampered checkpoint outputs were accepted")
	}
}
