package retention

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"norn/v2/api/archive"
	"norn/v2/api/store"
)

func TestReviewReadBackBindsWholeArchiveSubject(t *testing.T) {
	objects, err := archive.OpenLocal(filepath.Join(t.TempDir(), "archive"), archive.MaxBundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	ctx := context.Background()
	intent := store.EvidenceIntent{SubjectID: "saga-review", App: "expected-app", OperationID: "expected-op", Sequence: 1}
	bundle := &archive.Bundle{Subject: archive.Subject{Kind: "saga", ID: intent.SubjectID, App: "other-app", OperationID: "other-op"}, Sequence: 1, SealedAt: time.Now().UTC()}
	data, err := archive.Seal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	key, err := archive.ObjectKey(archive.Subject{Kind: "saga", ID: intent.SubjectID, App: intent.App}, 1)
	if err != nil {
		t.Fatal(err)
	}
	info, err := objects.PutImmutable(ctx, key, data)
	if err != nil {
		t.Fatal(err)
	}
	a := &Archiver{Archive: objects}
	if _, err := a.readBack(ctx, info, intent); err == nil {
		t.Fatal("archive acknowledgement accepted a different app and operation subject")
	}
}

func TestReviewPruneCannotLeaveLegacyReadersAdmitted(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	op := f.finishedSaga("reader-compatibility", 2)
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("publish = %+v, %v", report, err)
	}
	f.archiver.Mode = ModePrune
	_, _ = f.archiver.RunOnce(ctx)
	var minimumReader int64
	if err := f.db.Pool.QueryRow(ctx, `SELECT minimum_reader_version FROM norn_schema_compatibility WHERE singleton`).Scan(&minimumReader); err != nil {
		t.Fatal(err)
	}
	// Contract 1 readers only read the hot table. Either refuse pruning until
	// compatibility advances, or durably exclude those readers when pruning.
	if f.hotCount(op.SagaID) != 2 && minimumReader <= 1 {
		t.Fatal("pruned historical evidence while legacy hot-only readers remain admitted")
	}
}

func TestReviewPruneHonorsHoldCreatedDuringVerification(t *testing.T) {
	f := newRetentionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	op := f.finishedSaga("concurrent-hold", 2)
	if report, err := f.archiver.RunOnce(ctx); err != nil || report.Published != 1 {
		t.Fatalf("publish = %+v, %v", report, err)
	}
	intent := f.intents(op.SagaID)[0]
	holdCommitted := false
	pruned, _, err := f.db.PruneVerifiedEvidence(ctx, intent.ID, 0, func(ctx context.Context, _ store.EvidenceIntent) error {
		_, err := f.db.Pool.Exec(ctx, `UPDATE operations SET metadata = metadata || '{"manualRecoveryRequired":true}'::jsonb WHERE id=$1`, op.ID)
		holdCommitted = err == nil
		return err
	})
	if holdCommitted && (pruned != 0 || f.hotCount(op.SagaID) != 2) {
		t.Fatalf("pruned %d events after recovery hold committed during verification (error %v)", pruned, err)
	}
}
