package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/archive"
	"norn/v2/api/config"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
	"norn/v2/api/retention"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// privateControlDB is a migrated control store on a private scoped server
// whose only sessions declare an archive-aware reader contract, as pruning
// requires (it holds while any undeclared control-role session exists).
func privateControlDB(t *testing.T) *store.DB {
	t.Helper()
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_control")
	config, err := pgxpool.ParseConfig(server.URL("norn_control"))
	if err != nil {
		t.Fatal(err)
	}
	store.DeclareReaderContract(config, "handler-test")
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := &store.DB{Pool: pool}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestArchivedSagaHistoryReadsAreAuthorizedCompleteAndFailClosed(t *testing.T) {
	db := privateControlDB(t)
	ctx := context.Background()
	hot := saga.NewPostgresStore(db.Pool)
	root := filepath.Join(t.TempDir(), "evidence")
	objects, err := archive.OpenLocal(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	sagaID := uuid.NewString()
	log := saga.NewWithID(hot, sagaID, "shop", "pipeline", "deploy")
	for _, message := range []string{"clone", "build", "submit"} {
		if err := log.Log(ctx, "step.complete", message, map[string]string{"step": message}); err != nil {
			t.Fatal(err)
		}
	}
	finished := time.Now().Add(-time.Hour)
	if err := db.InsertCompletedOperation(ctx, &model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "shop", SagaID: sagaID, Status: model.OperationSucceeded,
		Payload: map[string]interface{}{}, Metadata: map[string]interface{}{}, StartedAt: finished, FinishedAt: &finished, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	archiver := &retention.Archiver{DB: db, Archive: objects, Mode: retention.ModePrune, BackfillAfter: 0, BatchSize: 10}
	report, err := archiver.RunOnce(ctx)
	if err != nil || report.Published != 1 || report.Pruned != 3 {
		t.Fatalf("archive pass = %+v, %v", report, err)
	}
	history := &retention.HistoryStore{Hot: hot, DB: db, Archive: objects}
	h := New(db, nil, nil, nil, &config.Config{AppsDir: t.TempDir(), AuditSigningKey: strings.Repeat("e", 32)}, nil, nil, nil, history, nil, nil)

	read := func(app string, principal *AccessPrincipal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/apps/"+app+"/sagas/"+sagaID, nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app)
		route.URLParams.Add("sagaId", sagaID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		if principal != nil {
			req = WithAccessPrincipal(req, principal)
		}
		rec := httptest.NewRecorder()
		h.GetAppSagaHistory(rec, req)
		return rec
	}
	reader := &AccessPrincipal{Subject: "reader", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}}
	rec := read("shop", reader)
	var body struct {
		Events []saga.Event `json:"events"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Events) != 3 || body.Events[2].Message != "submit" {
		t.Fatalf("archived history = %d %s", rec.Code, rec.Body.String())
	}
	if rec := read("shop", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}
	if rec := read("shop", &AccessPrincipal{Subject: "writer", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeEventsRead}}); rec.Code != http.StatusForbidden {
		t.Fatalf("without api:read = %d", rec.Code)
	}
	if rec := read("shop", &AccessPrincipal{Subject: "ci", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}, App: "other"}); rec.Code != http.StatusForbidden {
		t.Fatalf("other app's credential = %d", rec.Code)
	}
	if rec := read("other", reader); rec.Code != http.StatusNotFound {
		t.Fatalf("saga under another app = %d", rec.Code)
	}
	// A server without the archive configured refuses pruned history rather
	// than serving the (now empty) hot rows as complete.
	h.sagaStore = &retention.HistoryStore{Hot: hot, DB: db}
	if rec := read("shop", reader); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "archived_history_unavailable") {
		t.Fatalf("unarchived server read = %d %s", rec.Code, rec.Body.String())
	}
	h.sagaStore = history
	// Corrupted archived history is refused, not served partially.
	intents, err := db.EvidenceIntentsForSubject(ctx, "saga", sagaID)
	if err != nil || len(intents) != 1 {
		t.Fatalf("intents = %+v, %v", intents, err)
	}
	path := filepath.Join(root, filepath.FromSlash(intents[0].ObjectKey))
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := read("shop", reader); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "archived_history_unavailable") {
		t.Fatalf("corrupted history = %d %s", rec.Code, rec.Body.String())
	}
}
