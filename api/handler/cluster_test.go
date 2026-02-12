package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/api/config"
	"norn/api/hub"
	"norn/api/model"
	"norn/api/store"
)

func newTestHandlerWithDB(t *testing.T) *Handler {
	t.Helper()
	url := os.Getenv("NORN_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://norn:norn@localhost:5432/norn_db?sslmode=disable"
	}
	db, err := store.Connect(url)
	if err != nil {
		t.Skipf("skipping DB test (cannot connect): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := store.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ws := hub.New(nil)
	go ws.Run()
	cfg := &config.Config{Port: "0"}
	return &Handler{db: db, ws: ws, cfg: cfg}
}

func clusterRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/cluster", func(r chi.Router) {
		r.Get("/nodes", h.ListClusterNodes)
		r.Post("/nodes", h.AddClusterNode)
		r.Get("/nodes/{nodeId}", h.GetClusterNode)
		r.Delete("/nodes/{nodeId}", h.RemoveClusterNode)
	})
	return r
}

func TestClusterNodeLifecycle(t *testing.T) {
	h := newTestHandlerWithDB(t)
	r := clusterRouter(h)

	// POST add node → 201
	body := `{"name":"test-node-1","provider":"hetzner","region":"fsn1","size":"cx22","role":"server"}`
	req := httptest.NewRequest("POST", "/api/cluster/nodes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("POST /nodes: status = %d, want 201; body = %s", w.Code, w.Body.String())
	}

	var created model.ClusterNode
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode created node: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created node has empty ID")
	}
	if created.Status != "provisioning" {
		t.Errorf("status = %q, want provisioning", created.Status)
	}

	// Ensure cleanup
	t.Cleanup(func() {
		req := httptest.NewRequest("DELETE", "/api/cluster/nodes/"+created.ID, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	})

	// GET list → has node
	req = httptest.NewRequest("GET", "/api/cluster/nodes", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /nodes: status = %d", w.Code)
	}

	var nodes []model.ClusterNode
	if err := json.NewDecoder(w.Body).Decode(&nodes); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Error("created node not found in list")
	}

	// GET by ID
	req = httptest.NewRequest("GET", "/api/cluster/nodes/"+created.ID, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /nodes/{id}: status = %d", w.Code)
	}

	var fetched model.ClusterNode
	if err := json.NewDecoder(w.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode fetched node: %v", err)
	}
	if fetched.Name != "test-node-1" {
		t.Errorf("name = %q, want test-node-1", fetched.Name)
	}

	// DELETE → 204
	req = httptest.NewRequest("DELETE", "/api/cluster/nodes/"+created.ID, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /nodes/{id}: status = %d", w.Code)
	}

	// GET after delete → 404
	req = httptest.NewRequest("GET", "/api/cluster/nodes/"+created.ID, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status = %d, want 404", w.Code)
	}
}

func TestAddClusterNode_BadJSON(t *testing.T) {
	h := newTestHandlerWithDB(t)
	r := clusterRouter(h)

	req := httptest.NewRequest("POST", "/api/cluster/nodes", bytes.NewBufferString("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestListClusterNodes_Empty(t *testing.T) {
	h := newTestHandlerWithDB(t)
	r := clusterRouter(h)

	// First, get any existing nodes so we can verify the response shape
	req := httptest.NewRequest("GET", "/api/cluster/nodes", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Verify response is valid JSON (either null or array)
	var raw json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
}
