package retention

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"norn/v2/api/archive"
	"norn/v2/api/internal/s3emulator"
)

// The whole outbox → publish → verify → prune → archive-aware read →
// index-recovery path runs unchanged over the Fleet object adapter. The
// store is an in-process emulator: this proves Norn's integration and
// fault handling, not any real object service.
func TestEvidenceArchiveLifecycleOverObjectAdapter(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	emulator, server := s3emulator.Start("norn-evidence", "archive-writer")
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	objects, err := archive.OpenObjectStore(ctx, archive.ObjectStoreConfig{Endpoint: endpoint.Host, Bucket: "norn-evidence", Prefix: "fleet", Region: "us-east-1",
		AccessKey: "archive-writer", SecretKey: "archive-secret", Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	f.archiver.Archive = objects
	op := f.finishedSaga("object-shop", 3)

	// Outage first: evidence stays pending and hot.
	emulator.Configure(func(e *s3emulator.Emulator) { e.FailWrites = true })
	if report, err := f.archiver.RunOnce(ctx); err != nil || len(report.PublishErrors) != 1 || f.hotCount(op.SagaID) != 3 {
		t.Fatalf("outage pass = %+v, %v", report, err)
	}
	emulator.Configure(func(e *s3emulator.Emulator) { e.FailWrites = false })
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("publish = %+v, %v", report, err)
	}
	intent := f.intents(op.SagaID)[0]
	if intent.State != "verified" || intent.ObjectKey == "" {
		t.Fatalf("intent = %+v", intent)
	}
	f.archiver.Mode = ModePrune
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Pruned != 3 || f.hotCount(op.SagaID) != 0 {
		t.Fatalf("prune = %+v, %v", report, err)
	}
	history := &HistoryStore{Hot: f.hot, DB: f.db, Archive: objects}
	if events, err := history.ListBySaga(ctx, op.SagaID); err != nil || len(events) != 3 {
		t.Fatalf("archived history = %d, %v", len(events), err)
	}
	// The index can be rebuilt from the object archive alone.
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM evidence_archive_intents`); err != nil {
		t.Fatal(err)
	}
	if recovery, err := RestoreIndex(ctx, f.db, objects, f.signer); err != nil || recovery.Restored != 1 || len(recovery.Rejected) != 0 {
		t.Fatalf("index recovery = %+v, %v", recovery, err)
	}
	if events, err := history.ListBySaga(ctx, op.SagaID); err != nil || len(events) != 3 {
		t.Fatalf("history after recovery = %d, %v", len(events), err)
	}
	// A tampered object is refused, never served partially.
	emulator.Tamper("fleet/"+intent.ObjectKey, []byte(`{"schema":"forged"}`))
	var unavailable *ErrArchivedHistoryUnavailable
	if _, err := history.ListBySaga(ctx, op.SagaID); !errors.As(err, &unavailable) {
		t.Fatalf("tampered history = %v", err)
	}
}
