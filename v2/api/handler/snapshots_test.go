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

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/pipeline"
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

func TestDirectSnapshotMutationsAreRefusedWhileDatabaseTargetsAreConfigured(t *testing.T) {
	h := &Handler{cfg: &config.Config{AppsDir: t.TempDir()}, pipeline: &pipeline.Pipeline{DatabaseTargets: &pipeline.DatabaseTargets{ProfileID: "mini-local"}}}
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		// Import is not listed: with a profile it takes the target-bound
		// import path (manifest + digest), which mutates no database.
		"restore": h.RestoreSnapshot, "retention": h.ApplySnapshotRetention,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/apps/contextdb/snapshots/20260605T171500/restore?confirm=true", nil)
		req = withSnapshotTS(withAppID(req, "contextdb"), "20260605T171500")
		rec := httptest.NewRecorder()
		call(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "database_targets_active") {
			t.Fatalf("%s status = %d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	// The unconfigured-store import never falls back to the flat v2 import.
	req := withAppID(httptest.NewRequest(http.MethodPost, "/api/apps/contextdb/snapshots/import", strings.NewReader(`{"key":"snapshots/contextdb/x.dump"}`)), "contextdb")
	rec := httptest.NewRecorder()
	h.ImportSnapshot(rec, req)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "imported") {
		t.Fatalf("import with a profile = %d %s", rec.Code, rec.Body.String())
	}
}

// Recording a database baseline is a platform:operate mutation that needs a
// database profile; api:write alone and a missing profile are refused before
// anything is accepted.
func TestDatabaseBaselineRequiresPlatformScopeAndProfile(t *testing.T) {
	h := &Handler{cfg: &config.Config{AppsDir: t.TempDir()}, pipeline: &pipeline.Pipeline{}}
	serve := func(scopes []string) *httptest.ResponseRecorder {
		req := withAppID(httptest.NewRequest(http.MethodPost, "/api/v1/apps/shop/databases/baseline", strings.NewReader(`{"confirm":true}`)), "shop")
		req.Header.Set("Idempotency-Key", "baseline-1")
		req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", Source: AccessPrincipalSourceSharedAPI, Scopes: scopes})
		rec := httptest.NewRecorder()
		h.RecordDatabaseBaseline(rec, req)
		return rec
	}
	if rec := serve([]string{ScopeAPIRead, ScopeAPIWrite}); rec.Code != http.StatusForbidden {
		t.Fatalf("api:write baseline = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve([]string{ScopePlatformOperate}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "database_profile_not_configured") {
		t.Fatalf("baseline without a profile = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSnapshotRetentionUsesSpecPolicyDefault(t *testing.T) {
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
snapshots:
  keep: 2
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
	for _, name := range []string{
		"hermes_contextdb_aaaaaa_20260605T171500.dump",
		"hermes_contextdb_bbbbbb_20260606T171500.dump",
		"hermes_contextdb_cccccc_20260607T171500.dump",
	} {
		if err := os.WriteFile(filepath.Join(root, "snapshots", name), []byte("dump"), 0o644); err != nil {
			t.Fatal(err)
		}
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
	req := httptest.NewRequest(http.MethodPost, "/api/apps/contextdb/snapshots/retention", nil)
	req = withAppID(req, "contextdb")
	rec := httptest.NewRecorder()

	h.ApplySnapshotRetention(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var receipt snapshotRetentionReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Keep != 2 || len(receipt.Kept) != 2 || len(receipt.WouldPrune) != 1 {
		t.Fatalf("receipt = %+v, want keep=2 kept=2 wouldPrune=1", receipt)
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
