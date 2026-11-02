package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/internal/integrationtest"
)

func TestEtcdPlatformOperatorPairingLane(t *testing.T) {
	client, prefix := integrationtest.Etcd(t)
	identities := etcdstore.NewAuthStore(client, prefix)
	const secret = "0123456789abcdef0123456789abcdef"
	cfg := &config.Config{APIToken: secret, FleetPlatformReleaseIntentEnabled: true}
	admin, err := laneEtcdToken(secret, identities, "temporary-admin", "", handler.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	root := "/api/v1/fleet/platform-operator"
	operationID := uuid.NewString()
	router := chi.NewRouter()
	registerEtcdPlatformOperatorEnrollmentRoutes(router, cfg, identities)
	registerEtcdFleetExecutorEnrollmentRoutes(router, cfg, identities, true)
	call := func(method, path, bearer string, body interface{}) (int, []byte) {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "https://control.example"+path, bytes.NewReader(payload))
		req.TLS = &tls.ConnectionState{}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	start := func(platform string, scopes []string) (int, map[string]interface{}) {
		code, body := call(http.MethodPost, root+"/enrollments", "", map[string]interface{}{
			"deviceName": handler.FleetDeploymentRecoverySubjectPrefix + operationID, "platform": platform,
			"publicKey": laneECPublicKey(), "requestedScopes": scopes})
		var value map[string]interface{}
		_ = json.Unmarshal(body, &value)
		return code, value
	}
	for _, tc := range []struct {
		platform string
		scopes   []string
	}{
		{"ios", []string{handler.ScopeFleetDeploymentRecover}},
		{handler.FleetPlatformOperatorPlatform, []string{handler.ScopeAPIRead}},
		{handler.FleetPlatformOperatorPlatform, []string{handler.ScopePlatformOperate}},
		{handler.FleetPlatformOperatorPlatform, []string{handler.ScopeFleetDeploymentRecover, handler.ScopeAdmin}},
	} {
		if code, _ := start(tc.platform, tc.scopes); code != http.StatusBadRequest {
			t.Fatalf("wrong pairing platform=%q scopes=%v status=%d", tc.platform, tc.scopes, code)
		}
	}
	if code, _ := call(http.MethodPost, root+"/enrollments", "", map[string]interface{}{
		"deviceName": "unbound operator", "platform": handler.FleetPlatformOperatorPlatform,
		"publicKey": laneECPublicKey(), "requestedScopes": []string{handler.ScopeFleetDeploymentRecover}}); code != http.StatusBadRequest {
		t.Fatalf("unbound operator name status=%d", code)
	}
	code, executorStartBody := call(http.MethodPost, "/api/v1/enrollments", "", map[string]interface{}{
		"deviceName": "executor", "platform": handler.FleetExecutorPlatform,
		"publicKey": laneECPublicKey(), "requestedScopes": []string{handler.ScopeFleetExecute}})
	var executorStart map[string]interface{}
	_ = json.Unmarshal(executorStartBody, &executorStart)
	if code != http.StatusCreated {
		t.Fatalf("executor start status=%d", code)
	}
	if code, _ := call(http.MethodPost, root+"/enrollments/approve", admin,
		map[string]interface{}{"userCode": executorStart["userCode"], "scopes": []string{handler.ScopeFleetDeploymentRecover}}); code != http.StatusBadRequest {
		t.Fatalf("operator route approved executor code: status=%d", code)
	}
	code, started := start(handler.FleetPlatformOperatorPlatform, []string{handler.ScopeFleetDeploymentRecover})
	if code != http.StatusCreated || started["verificationPath"] != root+"/enrollments/approve" {
		t.Fatalf("operator start status=%d verificationPath=%v", code, started["verificationPath"])
	}
	id, _ := started["id"].(string)
	userCode, _ := started["userCode"].(string)
	verifier, _ := started["verifier"].(string)
	if id == "" || userCode == "" || verifier == "" {
		t.Fatal("operator pairing identifiers missing")
	}
	if code, _ := call(http.MethodPost, root+"/enrollments/approve", "", map[string]interface{}{"userCode": userCode}); code != http.StatusUnauthorized {
		t.Fatalf("unprivileged approval status=%d", code)
	}
	code, body := call(http.MethodPost, root+"/enrollments/approve", admin,
		map[string]interface{}{"userCode": userCode, "scopes": []string{handler.ScopeFleetDeploymentRecover}})
	if code != http.StatusOK || !strings.Contains(string(body), handler.ScopeFleetDeploymentRecover) {
		t.Fatalf("admin approval status=%d", code)
	}
	code, body = call(http.MethodPost, root+"/enrollments/"+id+"/exchange", "", map[string]string{"verifier": verifier})
	var issued struct {
		Token    string   `json:"token"`
		DeviceID string   `json:"deviceId"`
		Scopes   []string `json:"scopes"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &issued) != nil || issued.Token == "" || issued.DeviceID == "" || len(issued.Scopes) != 1 || issued.Scopes[0] != handler.ScopeFleetDeploymentRecover {
		t.Fatalf("operator exchange status=%d device=%t scopes=%v", code, issued.DeviceID != "", issued.Scopes)
	}
	if p, ok := handler.VerifyAccessTokenWithIdentityStore(secret, issued.Token, cfg.LegacyTokenSigningUntil, identities); !ok || p.DeviceID != issued.DeviceID || p.CI != nil || p.Subject != handler.FleetDeploymentRecoverySubjectPrefix+operationID {
		t.Fatal("operator credential not backed by active device registry")
	}
	if code, _ := call(http.MethodDelete, root+"/devices/"+issued.DeviceID, admin, nil); code != http.StatusNoContent {
		t.Fatalf("operator device revoke status=%d", code)
	}
	if _, ok := handler.VerifyAccessTokenWithIdentityStore(secret, issued.Token, cfg.LegacyTokenSigningUntil, identities); ok {
		t.Fatal("revoked operator credential remains valid")
	}
	defaultOff := chi.NewRouter()
	registerEtcdPlatformOperatorEnrollmentRoutes(defaultOff, &config.Config{APIToken: secret}, identities)
	req := httptest.NewRequest(http.MethodPost, root+"/enrollments", nil)
	rec := httptest.NewRecorder()
	defaultOff.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled operator lane status=%d", rec.Code)
	}
}
