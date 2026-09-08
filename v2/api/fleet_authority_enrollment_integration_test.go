package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

func TestFleetAuthorityManagementShellAndCapabilityContract(t *testing.T) {
	cfg := fleetAuthorityOnlyTestConfig(t)
	cfg.UIDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.UIDir, "index.html"), []byte("management-shell"), 0600); err != nil {
		t.Fatal(err)
	}
	router := fleetAuthorityOnlyRouter(cfg, nil)
	for path, want := range map[string]int{"/": 200, "/fleet": 200, "/api/v1/apps": 404, "/api/unknown": 404, "/ws": 404} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s status=%d want=%d", path, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/capabilities", nil))
	var caps struct {
		Authority string            `json:"authority"`
		Features  []string          `json:"features"`
		Auth      map[string]any    `json:"auth"`
		Endpoints map[string]string `json:"endpoints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Authority != "fleet-only" || !containsCapability(caps.Features, "fleet-authority-only-v1") {
		t.Fatal("authority discriminator absent")
	}
	for _, name := range []string{"enrollments", "devices", "tokenRotate", "tokenRevoke", "operationList", "activeOperations"} {
		if caps.Endpoints[name] == "" {
			t.Fatalf("missing %s endpoint", name)
		}
	}
	if caps.Auth["websocketBearerHeader"] != false || caps.Auth["websocketQueryToken"] != false || caps.Auth["deviceEnrollment"] != true {
		t.Fatal("inaccurate authority auth capabilities")
	}
}

// Uses an explicitly supplied disposable database, never the developer runtime.
func TestFleetAuthorityDeviceLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("NORN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
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
	// Enrollment source rate limiting is intentionally durable. Give this
	// disposable-DB integration run a unique authority secret so repeated and
	// parallel PG16 gates cannot inherit an earlier run's source bucket.
	cfg.APIToken += "-" + uuid.NewString()
	server := httptest.NewTLSServer(fleetAuthorityOnlyRouter(cfg, db))
	t.Cleanup(server.Close)
	call := func(method, path, token string, body any, want int) map[string]any {
		t.Helper()
		payload, _ := json.Marshal(body)
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d", method, path, res.StatusCode, want)
		}
		var data map[string]any
		if res.StatusCode != http.StatusNoContent {
			_ = json.NewDecoder(res.Body).Decode(&data)
		}
		return data
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	public, _ := key.PublicKey.Bytes()
	started := call("POST", "/api/v1/enrollments", "", map[string]any{
		"deviceName": "fleet-test-viewer", "publicKey": base64.RawURLEncoding.EncodeToString(public), "requestedScopes": []string{"api:read"},
	}, http.StatusCreated)
	id := started["id"].(string)
	code := started["userCode"].(string)
	verifier := map[string]string{"verifier": started["verifier"].(string)}
	call("POST", "/api/v1/enrollments/"+id+"/exchange", "", verifier, http.StatusConflict)
	call("POST", "/api/v1/enrollments/approve", cfg.APIToken, map[string]any{"userCode": code, "scopes": []string{"api:read", "api:write"}}, http.StatusBadRequest)
	call("POST", "/api/v1/enrollments/approve", cfg.APIToken, map[string]any{"userCode": code, "scopes": []string{"api:read"}}, http.StatusOK)
	issued := call("POST", "/api/v1/enrollments/"+id+"/exchange", "", verifier, http.StatusOK)
	token := issued["token"].(string)
	call("POST", "/api/v1/enrollments/"+id+"/exchange", "", verifier, http.StatusConflict)
	call("GET", "/api/v1/fleet/node-pools", token, nil, http.StatusOK)
	call("POST", "/api/v1/fleet/node-pools/control/plan", token, map[string]string{"reason": "viewer cannot change"}, http.StatusForbidden)
	call("GET", "/api/v1/devices", token, nil, http.StatusForbidden)
	call("GET", "/api/v1/audit/mutations", token, nil, http.StatusForbidden)
	call("POST", "/api/v1/fleet/plans/unknown/attempts", token, map[string]any{}, http.StatusForbidden)
	rotated := call("POST", "/api/v1/auth/rotate", token, map[string]any{}, http.StatusOK)
	call("GET", "/api/v1/fleet/node-pools", token, nil, http.StatusUnauthorized)
	newToken := rotated["token"].(string)
	call("GET", "/api/v1/fleet/node-pools", newToken, nil, http.StatusOK)
	call("DELETE", "/api/v1/devices/"+issued["deviceId"].(string), cfg.APIToken, nil, http.StatusNoContent)
	call("GET", "/api/v1/fleet/node-pools", newToken, nil, http.StatusUnauthorized)

	operator := call("POST", "/api/v1/enrollments", "", map[string]any{
		"deviceName": "fleet-test-operator", "publicKey": base64.RawURLEncoding.EncodeToString(public), "requestedScopes": []string{"api:read", "api:write"},
	}, http.StatusCreated)
	call("POST", "/api/v1/enrollments/approve", cfg.APIToken, map[string]any{"userCode": operator["userCode"]}, http.StatusOK)
	operatorIssued := call("POST", "/api/v1/enrollments/"+operator["id"].(string)+"/exchange", "", map[string]any{"verifier": operator["verifier"]}, http.StatusOK)
	operatorToken := operatorIssued["token"].(string)
	plan := call("POST", "/api/v1/fleet/node-pools/control/plan", operatorToken, map[string]any{"desired": 3, "reason": "review reconciliation without provider mutation"}, http.StatusCreated)
	planID := plan["id"].(string)
	call("GET", "/api/v1/operations/"+planID, operatorToken, nil, http.StatusOK)
	call("POST", "/api/v1/fleet/plans/"+planID+"/attempts", operatorToken, map[string]any{}, http.StatusForbidden)
	call("POST", "/api/v1/fleet/plans/"+planID+"/reconciliations", operatorToken, map[string]any{}, http.StatusForbidden)
	call("POST", "/api/v1/auth/revoke", operatorToken, map[string]any{}, http.StatusNoContent)
	call("GET", "/api/v1/fleet/node-pools", operatorToken, nil, http.StatusUnauthorized)
}
