package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/startup"
	"norn/v2/api/store"
)

// This is deliberately a real PostgreSQL test: finalize must use the durable
// epoch/owner CAS, not merely an in-memory HTTP flag. It creates an isolated
// schema and never contacts the Mini.
func TestLegacyBaselineMaintenanceReadAndFinalizeBoundary(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NORN_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "norn_legacy_maintenance_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") }()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &store.DB{Pool: pool}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}

	// Empty app discovery is a valid preservation population. Nomad and Consul
	// are observer-only in this mode; their URLs only need parse successfully.
	cfg := &config.Config{BindAddr: "127.0.0.1", APIToken: strings.Repeat("t", 32), AppsDir: t.TempDir(), NomadAddr: "http://127.0.0.1:4646", ConsulAddr: "http://127.0.0.1:8500", WorkloadConnector: "nomad-consul"}
	h, err := newLegacyBaselineMaintenanceHandler(cfg, db, "test-database", store.SchemaStatus{CurrentMigrationVersion: 1}, startup.Config{StartupMode: startup.ModeActive, SchemaMode: startup.SchemaModeCheck}, "transition-test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	client := server.Client()
	request := func(method, path string, body []byte, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	for _, path := range []string{"/api/health", "/api/apps", "/api/services/manifest", "/api/v1/platform/legacy-baseline/status"} {
		response := request(http.MethodGet, path, nil, cfg.APIToken)
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("authenticated GET %s = %d", path, response.StatusCode)
		}
		response.Body.Close()
	}
	unauthorized := request(http.MethodGet, "/api/apps", nil, "")
	if unauthorized.StatusCode != http.StatusUnauthorized {
		unauthorized.Body.Close()
		t.Fatalf("unauthenticated read = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()
	var status struct {
		Owner     string `json:"owner"`
		Epoch     int64  `json:"epoch"`
		Finalized bool   `json:"finalized"`
	}
	statusResponse := request(http.MethodGet, "/api/v1/platform/legacy-baseline/status", nil, cfg.APIToken)
	if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
		statusResponse.Body.Close()
		t.Fatal(err)
	}
	statusResponse.Body.Close()
	if status.Owner != "legacy-baseline:transition-test" || status.Epoch < 1 || status.Finalized {
		t.Fatalf("initial maintenance status = %+v", status)
	}
	unauthorizedFinalize := request(http.MethodPost, "/api/v1/platform/legacy-baseline/finalize", []byte(`{}`), "")
	if unauthorizedFinalize.StatusCode != http.StatusUnauthorized {
		unauthorizedFinalize.Body.Close()
		t.Fatalf("unauthorized finalize = %d", unauthorizedFinalize.StatusCode)
	}
	unauthorizedFinalize.Body.Close()
	wrong, _ := json.Marshal(map[string]any{"transitionId": "transition-test", "owner": status.Owner, "epoch": status.Epoch + 1})
	mismatch := request(http.MethodPost, "/api/v1/platform/legacy-baseline/finalize", wrong, cfg.APIToken)
	if mismatch.StatusCode != http.StatusBadRequest {
		mismatch.Body.Close()
		t.Fatalf("mismatched finalize = %d", mismatch.StatusCode)
	}
	mismatch.Body.Close()
	active, err := db.RuntimeMutationFenceActive(ctx)
	if err != nil || !active {
		t.Fatalf("mismatched finalize released fence: active=%t err=%v", active, err)
	}
	exact, _ := json.Marshal(map[string]any{"transitionId": "transition-test", "owner": status.Owner, "epoch": status.Epoch})
	finalize := request(http.MethodPost, "/api/v1/platform/legacy-baseline/finalize", exact, cfg.APIToken)
	if finalize.StatusCode != http.StatusOK {
		finalize.Body.Close()
		t.Fatalf("exact finalize = %d", finalize.StatusCode)
	}
	finalize.Body.Close()
	active, err = db.RuntimeMutationFenceActive(ctx)
	if err != nil || !active {
		t.Fatalf("maintenance finalize released the fence before normal activation: active=%t err=%v", active, err)
	}
	fence, active, err := db.RuntimeMutationFenceState(ctx)
	if err != nil || !active || fence.Owner != status.Owner || fence.Epoch != status.Epoch || fence.Reason != "legacy baseline finalization requested" {
		t.Fatalf("exact finalize did not retain the durable handoff fence: fence=%+v active=%t err=%v", fence, active, err)
	}
	second := request(http.MethodPost, "/api/v1/platform/legacy-baseline/finalize", exact, cfg.APIToken)
	if second.StatusCode != http.StatusConflict {
		second.Body.Close()
		t.Fatalf("second finalize = %d", second.StatusCode)
	}
	second.Body.Close()
}
