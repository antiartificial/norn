package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/store"
)

func TestPassiveBinaryStartupIsReadOnlyAndStatusOnly(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "norn_passive_startup_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+identifier+` CASCADE`); err != nil {
			t.Errorf("drop passive test schema: %v", err)
		}
	}()
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations(id,kind,app,saga_id,status,payload,metadata,attempts,max_attempts,locked_by,lock_generation,locked_until,next_attempt_at,started_at,updated_at)
		VALUES ('passive-expired-operation','app.preflight','demo','passive-saga','running','{}','{"evidence":"stable-receipt-bytes"}',1,3,'old-owner',4,now()-interval '1 minute',now(),now(),now());
		INSERT INTO mutation_audit_events(id,method,path,status,outcome,record_digest,key_id)
		VALUES ('passive-audit','POST','/api/apps/demo/deploy',202,'success','stable-audit-digest-bytes','audit-key-v1');
	`); err != nil {
		t.Fatal(err)
	}
	var beforeCompatibility time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM norn_schema_compatibility WHERE singleton`).Scan(&beforeCompatibility); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "norn-api")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build passive binary: %v\n%s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var nomadRequests atomic.Int64
	nomadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nomadRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer nomadServer.Close()
	var consulRequests atomic.Int64
	consulServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		consulRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer consulServer.Close()

	cmd := exec.Command(binary)
	cmd.Env = append(sanitizedStartupTestEnv(),
		"NORN_DATABASE_URL="+databaseURLWithSearchPath(t, databaseURL, schema, true),
		"NORN_BIND_ADDR=127.0.0.1",
		fmt.Sprintf("NORN_PORT=%d", port),
		"NORN_STARTUP_MODE=passive",
		"NORN_SCHEMA_MODE=check",
		"NORN_SCHEMA_TIMEOUT=10s",
		"NORN_NOMAD_ADDR="+nomadServer.URL,
		"NORN_CONSUL_ADDR="+consulServer.URL,
	)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: time.Second}
	var health map[string]any
	for deadline := time.Now().Add(10 * time.Second); ; {
		response, requestErr := client.Get(baseURL + "/api/health")
		if requestErr == nil {
			decodeErr := json.NewDecoder(response.Body).Decode(&health)
			_ = response.Body.Close()
			if decodeErr == nil && response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			stopped = true
			t.Fatalf("passive API did not become ready: %v\n%s", requestErr, logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if health["status"] != "passive" {
		t.Fatalf("health = %#v", health)
	}
	for _, request := range []struct{ method, path string }{{http.MethodGet, "/api/apps"}, {http.MethodPost, "/api/apps/demo/deploy"}, {http.MethodPost, "/api/webhooks/github"}} {
		req, err := http.NewRequest(request.method, baseURL+request.path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want rejected", request.method, request.path, response.StatusCode)
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("passive API exit: %v\n%s", err, logs.String())
	}
	stopped = true

	var operationStatus, operationOwner, operationEvidence, auditDigest string
	var operationGeneration int64
	if err := pool.QueryRow(ctx, `SELECT status,locked_by,lock_generation,metadata->>'evidence' FROM operations WHERE id='passive-expired-operation'`).Scan(&operationStatus, &operationOwner, &operationGeneration, &operationEvidence); err != nil {
		t.Fatal(err)
	}
	if operationStatus != "running" || operationOwner != "old-owner" || operationGeneration != 4 || operationEvidence != "stable-receipt-bytes" {
		t.Fatalf("passive startup mutated operation: status=%q owner=%q generation=%d evidence=%q", operationStatus, operationOwner, operationGeneration, operationEvidence)
	}
	if err := pool.QueryRow(ctx, `SELECT record_digest FROM mutation_audit_events WHERE id='passive-audit'`).Scan(&auditDigest); err != nil {
		t.Fatal(err)
	}
	if auditDigest != "stable-audit-digest-bytes" {
		t.Fatalf("passive startup mutated audit digest: %q", auditDigest)
	}
	var afterCompatibility time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM norn_schema_compatibility WHERE singleton`).Scan(&afterCompatibility); err != nil {
		t.Fatal(err)
	}
	if !afterCompatibility.Equal(beforeCompatibility) {
		t.Fatalf("passive schema check mutated compatibility metadata: before=%s after=%s", beforeCompatibility, afterCompatibility)
	}
	if nomadRequests.Load() != 0 || consulRequests.Load() != 0 {
		t.Fatalf("passive startup contacted runtime clients: nomad=%d consul=%d", nomadRequests.Load(), consulRequests.Load())
	}
	for _, unexpected := range []string{"nomad connected", "consul connected", "operation worker", "operation recovery"} {
		if strings.Contains(strings.ToLower(logs.String()), unexpected) {
			t.Fatalf("passive startup initialized runtime component %q:\n%s", unexpected, logs.String())
		}
	}
}

func sanitizedStartupTestEnv() []string {
	allowed := []string{}
	for _, entry := range os.Environ() {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		if key == "PATH" || key == "HOME" || key == "TMPDIR" {
			allowed = append(allowed, entry)
		}
	}
	return allowed
}

func databaseURLWithSearchPath(t *testing.T, databaseURL, schema string, readOnly bool) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	if readOnly {
		query.Set("default_transaction_read_only", "on")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
