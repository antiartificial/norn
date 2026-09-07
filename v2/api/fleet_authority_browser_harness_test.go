package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"norn/v2/api/store"
)

// TestFleetAuthorityBrowserHarness is deliberately opt-in. It serves the
// built UI against a caller-supplied disposable test database, with no cloud
// provider, GitHub, runner, or workload action. Run only when a human is ready
// to exercise pairing locally:
//
//	go test -c -o /tmp/norn-browser-harness . && \
//	NORN_FLEET_AUTHORITY_BROWSER_HARNESS=1 NORN_TEST_DATABASE_URL=... \
//	  /tmp/norn-browser-harness -test.run '^TestFleetAuthorityBrowserHarness$' -test.v
//
// The only output is the loopback port. Enter `approve CODE` on stdin after
// the browser presents its code, or `quit` to stop. The bootstrap admin token
// remains inside this test process and is never logged.
func TestFleetAuthorityBrowserHarness(t *testing.T) {
	if os.Getenv("NORN_FLEET_AUTHORITY_BROWSER_HARNESS") != "1" {
		t.Skip("set NORN_FLEET_AUTHORITY_BROWSER_HARNESS=1 to run the local browser harness")
	}
	dsn := strings.TrimSpace(os.Getenv("NORN_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Fatal("NORN_TEST_DATABASE_URL must name a disposable database")
	}
	uiDir := strings.TrimSpace(os.Getenv("NORN_FLEET_AUTHORITY_UI_DIR"))
	if uiDir == "" {
		uiDir = filepath.Clean(filepath.Join("..", "ui", "dist"))
	}
	if info, err := os.Stat(filepath.Join(uiDir, "index.html")); err != nil || info.IsDir() {
		t.Fatalf("built UI is required at %s (run pnpm build first)", uiDir)
	}

	db, err := store.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}

	cfg := fleetAuthorityOnlyTestConfig(t)
	cfg.DatabaseURL = dsn
	cfg.UIDir = uiDir
	cfg.BindAddr = "127.0.0.1"
	cfg.APIToken = freshHarnessToken(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	cfg.AllowedOrigins = "http://127.0.0.1:" + strconv.Itoa(port)
	server := &http.Server{Handler: fleetAuthorityOnlyRouter(cfg, db), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	})

	t.Logf("HARNESS_PORT=%d", port)

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 1 && fields[0] == "quit" {
			return
		}
		if len(fields) == 2 && fields[0] == "approve" {
			approveBrowserEnrollment(t, listener.Addr().String(), cfg.APIToken, fields[1])
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func freshHarnessToken(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func approveBrowserEnrollment(t *testing.T, address, adminToken, code string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"userCode": code, "scopes": []string{"api:read"}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+address+"/api/v1/enrollments/approve", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("enrollment approval failed with status %d", res.StatusCode)
	}
}
