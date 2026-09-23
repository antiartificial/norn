package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/archive"
	"norn/v2/api/config"
	"norn/v2/api/model"
	"norn/v2/api/retention"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// Audited mutations are refused while the durable evidence reserve is
// exhausted — by the live outbox backlog or by the archive's recorded
// capacity — and admitted again once evidence is archived. The refusal is
// itself audited; reads and token revocation are not blocked.
func TestEvidenceReserveRefusesAuditedMutationsUntilEvidenceIsArchived(t *testing.T) {
	db := privateControlDB(t)
	ctx := context.Background()
	h := New(db, nil, nil, nil, &config.Config{AppsDir: t.TempDir(), AuditSigningKey: strings.Repeat("r", 32)}, nil, nil, nil, saga.NewPostgresStore(db.Pool), nil, nil)
	reached := 0
	chain := h.MutationAuditMiddleware(h.EvidenceReserveAdmissionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusAccepted)
	})))
	send := func(method, path string) *httptest.ResponseRecorder {
		h.evidenceReserveAt = time.Time{} // no cached admission view between steps
		req := httptest.NewRequest(method, path, nil)
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAdmin}})
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec
	}
	// Unarchived evidence accumulates: two finished sagas with outbox rows.
	hot := saga.NewPostgresStore(db.Pool)
	for range 2 {
		sagaID := uuid.NewString()
		if err := saga.NewWithID(hot, sagaID, "shop", "pipeline", "deploy").Log(ctx, "step.complete", "build", nil); err != nil {
			t.Fatal(err)
		}
		finished := time.Now().Add(-time.Hour)
		if err := db.InsertCompletedOperation(ctx, &model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "shop", SagaID: sagaID, Status: model.OperationSucceeded,
			Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, StartedAt: finished, FinishedAt: &finished, MaxAttempts: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.BackfillEvidenceIntents(ctx, 0, 10); err != nil {
		t.Fatal(err)
	}
	// Without an enabled policy nothing is enforced.
	if rec := send(http.MethodPost, "/api/v1/apps/shop/deploy"); rec.Code != http.StatusAccepted {
		t.Fatalf("disabled reserve = %d %s", rec.Code, rec.Body.String())
	}
	if err := db.SetEvidenceReservePolicy(ctx, store.EvidenceReservePolicy{Enabled: true, MaxPending: 2, MaxPendingAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	before := reached
	rec := send(http.MethodPost, "/api/v1/apps/shop/deploy")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "evidence_reserve_exhausted") || !strings.Contains(rec.Body.String(), "2 unarchived") || reached != before {
		t.Fatalf("backlog over reserve = %d %s", rec.Code, rec.Body.String())
	}
	var refusedAudits int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM mutation_audit_events WHERE path = '/api/v1/apps/shop/deploy' AND status = 503`).Scan(&refusedAudits); err != nil || refusedAudits != 1 {
		t.Fatalf("refusal audit rows = %d, %v", refusedAudits, err)
	}
	if rec := send(http.MethodGet, "/api/v1/apps/shop"); rec.Code != http.StatusAccepted {
		t.Fatalf("read during exhaustion = %d", rec.Code)
	}
	if rec := send(http.MethodPost, "/api/v1/auth/revoke"); rec.Code != http.StatusAccepted {
		t.Fatalf("revocation during exhaustion = %d", rec.Code)
	}

	// Archiving the backlog restores admission; an archive whose recorded
	// headroom falls below the reserve exhausts it again, from any process.
	objects, err := archive.OpenLocal(filepath.Join(t.TempDir(), "evidence"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	archiver := &retention.Archiver{DB: db, Archive: objects, Mode: retention.ModeShadow, BatchSize: 10, MinFreeBytes: 1024}
	if report, err := archiver.RunOnce(ctx); err != nil || report.Published != 2 {
		t.Fatalf("archive pass = %+v, %v", report, err)
	}
	if rec := send(http.MethodPost, "/api/v1/apps/shop/deploy"); rec.Code != http.StatusAccepted {
		t.Fatalf("after archiving = %d %s", rec.Code, rec.Body.String())
	}
	archiver.MinFreeBytes = 1 << 20
	if _, err := archiver.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := send(http.MethodPut, "/api/v1/apps/shop/deployment"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "archive capacity is exhausted") {
		t.Fatalf("archive headroom exhausted = %d %s", rec.Code, rec.Body.String())
	}
	status, err := db.EvidenceReserve(ctx)
	if err != nil || !status.Exhausted || !status.ArchiveExhausted || status.Pending != 0 {
		t.Fatalf("reserve status = %+v, %v", status, err)
	}
}
