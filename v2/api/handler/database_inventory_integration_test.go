package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/database"
	"norn/v2/api/pipeline"
	"norn/v2/api/saga"
)

// Inventory, readiness and ops views read the same target-aware layer as
// restore when a profile is configured: only dumps whose provenance names
// the current target appear; same-name files of another target, unbound
// files and symlinks never do; a named app without a profile lists nothing.
func TestSnapshotInventoryViewsAreTargetAware(t *testing.T) {
	db := acceptanceIntegrationDB(t)
	ctx := context.Background()
	appsDir, snapshots := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(appsDir, "shop"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := "schemaVersion: norn.app/v2\nname: shop\ndeploy: true\nprocesses:\n  web:\n    command: x\ndatabases:\n  - name: primary\n    purpose: application\n    capabilities: [snapshot, restore]\n"
	if err := os.WriteFile(filepath.Join(appsDir, "shop", "infraspec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := handlerTestCatalog(1)
	if _, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator"); err != nil {
		t.Fatal(err)
	}
	target := database.TargetIdentity{ServiceID: "pg-main", ServiceGeneration: 1, BindingID: "shop-primary", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "shop", Role: "shop_app"}
	namespaceSum := sha256.Sum256([]byte(target.ServiceID + "\x00" + target.BindingID + "\x00" + string(target.Engine) + "\x00" + target.Database))
	namespace := filepath.Join(snapshots, "targets", hex.EncodeToString(namespaceSum[:16]))
	if err := os.MkdirAll(namespace, 0o750); err != nil {
		t.Fatal(err)
	}
	writeDump := func(dir, name string, identity *database.TargetIdentity) {
		t.Helper()
		content := []byte("dump " + name)
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
		if identity == nil {
			return
		}
		sum := sha256.Sum256(content)
		sidecar, _ := json.Marshal(map[string]interface{}{"schema": "norn.database-snapshot/v1", "target": identity, "catalogRevision": 1, "sha256": hex.EncodeToString(sum[:]), "size": len(content)})
		if err := os.WriteFile(filepath.Join(dir, name+".target.json"), sidecar, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeDump(namespace, "shop_manual_20260922T120000.dump", &target)
	foreign := target
	foreign.BindingGeneration = 7
	writeDump(namespace, "shop_manual_20260922T130000.dump", &foreign)
	writeDump(namespace, "shop_manual_20260922T140000.dump", nil)
	writeDump(snapshots, "shop_manual_20260922T150000.dump", nil) // v2 flat same-name file
	if err := os.Symlink(filepath.Join(namespace, "shop_manual_20260922T120000.dump"), filepath.Join(namespace, "shop_manual_20260922T160000.dump")); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{AppsDir: appsDir, AuditSigningKey: strings.Repeat("i", 32)}
	pipe := &pipeline.Pipeline{DB: db, SagaStore: saga.NewPostgresStore(db.Pool), AppsDir: appsDir,
		DatabaseTargets: &pipeline.DatabaseTargets{ProfileID: "mini", Catalog: db.ActiveDatabaseCatalog, SnapshotRoot: snapshots}}
	h := New(db, nil, nil, nil, cfg, pipe, nil, nil, pipe.SagaStore, nil, nil)
	list := func(handlerFunc http.HandlerFunc, path string) []snapshotEntry {
		t.Helper()
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "shop")
		req := WithAccessPrincipal(httptest.NewRequest(http.MethodGet, path, nil), &AccessPrincipal{Subject: "reader", Source: AccessPrincipalSourceSharedAPI, Scopes: []string{ScopeAPIRead}})
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rec := httptest.NewRecorder()
		handlerFunc(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
		var entries []snapshotEntry
		if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
			t.Fatal(err)
		}
		return entries
	}
	for _, entries := range [][]snapshotEntry{list(h.ListSnapshots, "/api/apps/shop/snapshots"), list(h.ListAppSnapshotsV1, "/api/v1/apps/shop/snapshots")} {
		if len(entries) != 1 || entries[0].Filename != "shop_manual_20260922T120000.dump" || entries[0].LogicalDatabase != "primary" ||
			entries[0].BindingID != "shop-primary" || entries[0].BindingGeneration != 1 || entries[0].Provenance != "sidecar" || entries[0].Database != "shop" {
			t.Fatalf("inventory = %+v", entries)
		}
	}
	shop := h.findSpec("shop")
	if summary := h.summarizeSnapshots(ctx, shop); summary == nil || summary.Count != 1 || summary.Database != "primary" {
		t.Fatalf("ops summary = %+v", summary)
	}
	readiness, err := h.buildOperatorSnapshotReadiness(httptest.NewRequest(http.MethodGet, "/api/operator/snapshots", nil))
	if err != nil || len(readiness.Apps) != 1 || readiness.Apps[0].Count != 1 || readiness.Apps[0].Database != "primary" || readiness.Apps[0].Status != "ready" {
		t.Fatalf("operator snapshot readiness = %+v, %v", readiness, err)
	}

	// The same named app without a profile has no inventory at all (never a
	// database-name listing of the flat directory).
	pipe.DatabaseTargets = nil
	if entries := h.snapshotsForSpec(ctx, shop); len(entries) != 0 {
		t.Fatalf("named inventory without a profile = %+v", entries)
	}
}
