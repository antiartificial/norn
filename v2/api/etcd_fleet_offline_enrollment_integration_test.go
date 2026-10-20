package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

// This drives the same mounted routes as the normal etcd runtime against its
// real identity aggregate: no PG store or pre-seeded offline bearer is used.
func TestEtcdOfflineEnrollmentMountedRoutes(t *testing.T) {
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/norn-test/offline-enrollment/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()); _ = client.Close() })
	identities := etcdstore.NewAuthStore(client, prefix)
	cfg := &config.Config{APIToken: strings.Repeat("s", 40), FleetAuthorityOnly: true, FleetOfflineReservationEnabled: true, FleetOfflineOwnerKeyID: "test-owner"}
	signer, err := store.NewHMACAcceptanceSigner(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := etcdstore.NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	registerEtcdFleetOfflineRoutes(router, cfg, identities, attempts, make(ed25519.PublicKey, ed25519.PublicKeySize), &reservationEvidenceVerifier{})
	adminRecord, err := handler.NewManagedAccessTokenRecord("test-operator", []string{handler.ScopeAdmin}, time.Hour, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := identities.RecordAccessToken(context.Background(), adminRecord); err != nil {
		t.Fatal(err)
	}
	adminToken, err := handler.SignManagedAccessToken(cfg.APIToken, adminRecord)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token, body string, trusted bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if trusted {
			req.TLS = &tls.ConnectionState{}
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	if response := call(http.MethodGet, "/api/v1/fleet/offline/plans/"+uuid.NewString()+"/reservation", "", "", true); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated reservation readback=%d", response.Code)
	}
	startPath := "/api/v1/fleet/offline/enrollments"
	request := `{"deviceName":"offline runner","platform":" FLEET-OFFLINE-RUNNER ","requestedScopes":["fleet:offline-attempt"],"publicKey":"BAD"}`
	if response := call(http.MethodPost, startPath, "", request, false); response.Code != http.StatusUpgradeRequired {
		t.Fatalf("plain external pairing=%d", response.Code)
	}
	request = `{"deviceName":"offline runner","platform":" FLEET-OFFLINE-RUNNER ","requestedScopes":["fleet:offline-attempt"]}`
	if response := call(http.MethodPost, startPath, "", strings.Replace(request, "fleet:offline-attempt", "fleet:operate", 1), true); response.Code != http.StatusBadRequest {
		t.Fatalf("broad scope=%d %s", response.Code, response.Body)
	}
	started := call(http.MethodPost, startPath, "", request, true)
	if started.Code != http.StatusCreated {
		t.Fatalf("start=%d %s", started.Code, started.Body)
	}
	var enrollment struct{ ID, UserCode, Verifier, VerificationPath, PollPath string }
	if err := json.Unmarshal(started.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	if enrollment.ID == "" || enrollment.UserCode == "" || enrollment.Verifier == "" || enrollment.VerificationPath != "/api/v1/fleet/offline/enrollments/approve" {
		t.Fatalf("bad enrollment: %+v", enrollment)
	}
	approved := call(http.MethodPost, enrollment.VerificationPath, adminToken, `{"userCode":"`+enrollment.UserCode+`"}`, true)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve=%d %s", approved.Code, approved.Body)
	}
	exchanged := call(http.MethodPost, enrollment.PollPath, "", `{"verifier":"`+enrollment.Verifier+`"}`, true)
	if exchanged.Code != http.StatusOK {
		t.Fatalf("exchange=%d %s", exchanged.Code, exchanged.Body)
	}
	var issued struct {
		Token, DeviceID string
		Scopes          []string
	}
	if err := json.Unmarshal(exchanged.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" || issued.DeviceID == "" || len(issued.Scopes) != 1 || issued.Scopes[0] != handler.ScopeFleetOfflineAttempt {
		t.Fatalf("bad issued token: %+v", issued)
	}
	principal, authenticated := handler.VerifyAccessTokenWithIdentityStore(cfg.APIToken, issued.Token, time.Time{}, identities)
	if !authenticated || principal.Subject != "fleet-offline-device:"+issued.DeviceID {
		t.Fatalf("offline bearer not bound to device ID: %+v", principal)
	}
	if again := call(http.MethodPost, enrollment.PollPath, "", `{"verifier":"`+enrollment.Verifier+`"}`, true); again.Code != http.StatusConflict {
		t.Fatalf("exchange replay=%d", again.Code)
	}
	device, err := identities.ActiveAccessDevice(context.Background(), issued.DeviceID)
	if err != nil || device.Platform != handler.FleetOfflineRunnerPlatform {
		t.Fatalf("platform=%+v err=%v", device, err)
	}
	attemptPath := "/api/v1/fleet/offline/plans/" + uuid.NewString() + "/attempt"
	if response := call(http.MethodGet, attemptPath, issued.Token, "", false); response.Code != http.StatusNotFound {
		t.Fatalf("mounted offline bearer=%d %s", response.Code, response.Body)
	}
	if response := call(http.MethodGet, attemptPath, adminToken, "", false); response.Code != http.StatusForbidden {
		t.Fatalf("admin reached offline attempt=%d", response.Code)
	}
	if response := call(http.MethodDelete, "/api/v1/fleet/offline/devices/"+issued.DeviceID, adminToken, "", false); response.Code != http.StatusNoContent {
		t.Fatalf("revoke=%d %s", response.Code, response.Body)
	}
	if response := call(http.MethodGet, attemptPath, issued.Token, "", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bearer=%d", response.Code)
	}
}
