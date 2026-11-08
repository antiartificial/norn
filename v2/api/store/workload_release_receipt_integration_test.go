package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestWorkloadReleaseReceiptCASIsClaimFencedAndIntegrityChecked(t *testing.T) {
	dbs, _, _ := setupEffectStores(t, 1)
	db := dbs[0]
	ctx := context.Background()
	if migration := workloadReleaseReceiptMigration(); migration.Version != 52 || migration.MinimumWriterVersion != ControlSchemaWriterVersion {
		t.Fatalf("receipt migration contract = %+v writer=%d", migration, ControlSchemaWriterVersion)
	}
	_, claim := claimEffectOperation(t, db, "workload-worker", time.Minute)
	first := json.RawMessage(`{"intentId":"intent-1","phase":"planned"}`)
	stored, err := db.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", "", first)
	if err != nil || stored.Revision != 1 || stored.ClaimGeneration != claim.Generation() || string(stored.Receipt) != string(first) {
		t.Fatalf("initial receipt = %+v, %v", stored, err)
	}
	loaded, err := db.LoadWorkloadReleaseReceipt(ctx, "intent-1")
	if err != nil || loaded == nil || loaded.ReceiptDigest != stored.ReceiptDigest || string(loaded.Receipt) != string(first) {
		t.Fatalf("loaded receipt = %+v, %v", loaded, err)
	}

	second := json.RawMessage(`{"intentId":"intent-1","phase":"warming"}`)
	advanced, err := db.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", stored.ReceiptDigest, second)
	if err != nil || advanced.Revision != 2 || advanced.ReceiptDigest == stored.ReceiptDigest {
		t.Fatalf("advanced receipt = %+v, %v", advanced, err)
	}
	if _, err := db.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", stored.ReceiptDigest, first); !errors.Is(err, ErrWorkloadReleaseReceiptConflict) {
		t.Fatalf("stale receipt digest CAS = %v", err)
	}
	if _, err := db.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", advanced.ReceiptDigest, json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed receipt accepted")
	}
	if _, err := db.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, "intent-1", advanced.ReceiptDigest, json.RawMessage(`{"intentId":"intent-other"}`)); err == nil {
		t.Fatal("receipt for a different intent accepted")
	}

	stale, err := NewOperationClaim(claim.OperationID(), claim.OwnerID(), claim.Generation()+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompareAndSwapWorkloadReleaseReceipt(ctx, stale, "intent-1", advanced.ReceiptDigest, first); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatalf("stale operation claim CAS = %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE workload_release_receipts SET receipt=convert_to('{"intentId":"forged"}','UTF8') WHERE intent_id='intent-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadWorkloadReleaseReceipt(ctx, "intent-1"); err == nil {
		t.Fatal("tampered receipt bytes were accepted")
	}
}
