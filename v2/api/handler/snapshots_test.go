package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
)

func TestParseSnapshotEntryHandlesDatabaseUnderscores(t *testing.T) {
	entry := parseSnapshotEntry("hermes_contextdb", "hermes_contextdb_abcdef_20260605T171500.dump", 42)
	if entry == nil {
		t.Fatal("entry is nil")
	}
	if entry.Database != "hermes_contextdb" {
		t.Fatalf("database = %q", entry.Database)
	}
	if entry.CommitSHA != "abcdef" {
		t.Fatalf("commit = %q", entry.CommitSHA)
	}
	if entry.Timestamp != "20260605T171500" {
		t.Fatalf("timestamp = %q", entry.Timestamp)
	}
	if entry.CreatedAt != "2026-06-05T17:15:00Z" {
		t.Fatalf("createdAt = %q", entry.CreatedAt)
	}
}

func TestListSnapshotsIncludesProvenance(t *testing.T) {
	root := t.TempDir()
	appsDir := filepath.Join(root, "apps")
	appDir := filepath.Join(appsDir, "contextdb")
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := []byte(`
name: contextdb
deploy: true
infrastructure:
  postgres:
    database: hermes_contextdb
processes:
  web:
    port: 7701
`)
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), spec, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "snapshots", "hermes_contextdb_abcdef_20260605T171500.dump"), []byte("dump"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: &config.Config{AppsDir: appsDir}}
	req := httptest.NewRequest(http.MethodGet, "/api/apps/contextdb/snapshots", nil)
	req = withAppID(req, "contextdb")
	rec := httptest.NewRecorder()
	h.ListSnapshots(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var snapshots []snapshotEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snapshots))
	}
	if snapshots[0].CommitSHA != "abcdef" || snapshots[0].CreatedAt == "" {
		t.Fatalf("snapshot provenance = %+v", snapshots[0])
	}
}

func TestRestoreSnapshotRequiresConfirmation(t *testing.T) {
	root := t.TempDir()
	appsDir := filepath.Join(root, "apps")
	appDir := filepath.Join(appsDir, "contextdb")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := []byte(`
name: contextdb
deploy: true
infrastructure:
  postgres:
    database: hermes_contextdb
processes:
  web:
    port: 7701
`)
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), spec, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{AppsDir: appsDir}}
	req := httptest.NewRequest(http.MethodPost, "/api/apps/contextdb/snapshots/20260605T171500/restore", nil)
	req = withAppID(req, "contextdb")
	req = withSnapshotTS(req, "20260605T171500")
	rec := httptest.NewRecorder()

	h.RestoreSnapshot(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func withAppID(r *http.Request, appID string) *http.Request {
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", appID)
	return r.WithContext(contextWithRoute(r, ctx))
}

func withSnapshotTS(r *http.Request, ts string) *http.Request {
	ctx := chi.RouteContext(r.Context())
	if ctx == nil {
		ctx = chi.NewRouteContext()
	}
	ctx.URLParams.Add("ts", ts)
	return r.WithContext(contextWithRoute(r, ctx))
}

func contextWithRoute(r *http.Request, ctx *chi.Context) context.Context {
	return context.WithValue(r.Context(), chi.RouteCtxKey, ctx)
}
