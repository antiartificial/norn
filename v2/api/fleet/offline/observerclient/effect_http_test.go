package observerclient

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
)

const testEffectChallengeID = "33333333-3333-4333-8333-333333333333"

func effectHTTPFixture() (offline.EffectChallengeV1, offline.SignedEffectAttestationV1, etcdstore.AcceptedOfflineEffectAttestation) {
	challenge := offline.EffectChallengeV1{SchemaVersion: offline.EffectChallengeSchemaV1, Purpose: offline.EffectIssuePurpose,
		ID: testEffectChallengeID, PlanID: testPlanID, AttemptID: testAttemptID}
	signed := offline.SignedEffectAttestationV1{Attestation: offline.EffectAttestationV1{
		SchemaVersion: offline.EffectAttestationSchemaV1, Purpose: offline.EffectIssuePurpose, ChallengeID: testEffectChallengeID,
		Observation: offline.EffectObservation{PlanID: testPlanID, AttemptID: testAttemptID}}, KeyID: "observer-key", Signature: "signature"}
	raw, _ := json.Marshal(signed)
	sum := sha256.Sum256(raw)
	receipt := etcdstore.AcceptedOfflineEffectAttestation{SchemaVersion: "norn.fleet-offline-effect-accepted/v1",
		PlanID: testPlanID, AttemptID: testAttemptID, Purpose: offline.EffectIssuePurpose, ChallengeID: testEffectChallengeID,
		AcceptedAt: time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC), AttestationSHA256: hex.EncodeToString(sum[:]),
		Envelope: raw, Challenge: challenge}
	return challenge, signed, receipt
}

func TestEffectHTTPClientExactPinnedObserverRoundTrip(t *testing.T) {
	challenge, signed, receipt := effectHTTPFixture()
	var methods []string
	terminal, _ := testHTTPClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer observer-secret" {
			t.Errorf("wrong observer bearer: %q", got)
		}
		if r.Host != "fleet.test" || r.URL.RawQuery != "" {
			t.Errorf("unpinned host or query: %s %s", r.Host, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		switch r.URL.Path {
		case "/api/v1/fleet/offline/attempts/" + testPlanID + "/" + testAttemptID + "/signed-effect/effect-issue/observer/challenge":
			if r.Method != http.MethodGet {
				t.Errorf("observer issued challenge via %s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(struct {
				Challenge offline.EffectChallengeV1 `json:"challenge"`
			}{challenge})
		case "/api/v1/fleet/offline/attempts/" + testPlanID + "/" + testAttemptID + "/signed-effect/effect-issue/observer/attestations/" + testEffectChallengeID:
			_ = json.NewEncoder(w).Encode(struct {
				Accepted etcdstore.AcceptedOfflineEffectAttestation `json:"accepted"`
			}{receipt})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	c := &EffectHTTPClient{terminal: terminal, paths: make(map[string]string)}
	if _, err := c.ReadAcceptedEffectAttestation(context.Background(), testPlanID, testAttemptID, testEffectChallengeID); err == nil {
		t.Fatal("accepted read without exact challenge-purpose binding")
	}
	if len(methods) != 0 {
		t.Fatal("unbound read sent to target")
	}
	if got, err := c.ReadEffectChallenge(context.Background(), testPlanID, testAttemptID, offline.EffectIssuePurpose); err != nil || got != challenge {
		t.Fatalf("challenge: %v", err)
	}
	got, err := c.ReadAcceptedEffectAttestation(context.Background(), testPlanID, testAttemptID, testEffectChallengeID)
	if err != nil || got.Signed != signed || !got.AcceptedAt.Equal(receipt.AcceptedAt) {
		t.Fatalf("accepted read: %v", err)
	}
	if err := c.SubmitEffectAttestation(context.Background(), testPlanID, testAttemptID, signed); err != nil {
		t.Fatalf("one attestation PUT: %v", err)
	}
	if len(methods) != 3 || !strings.HasPrefix(methods[2], "PUT ") {
		t.Fatalf("unexpected wire methods %v", methods)
	}
	terminal.client.Transport.(*http.Transport).TLSClientConfig.ServerName = "wrong.test"
	if _, err := c.ReadEffectChallenge(context.Background(), testPlanID, testAttemptID, offline.EffectIssuePurpose); err == nil {
		t.Fatal("wrong TLS name accepted")
	}
}

func TestEffectHTTPClientRejectsUntrustedAbsenceAndReceipt(t *testing.T) {
	challenge, _, receipt := effectHTTPFixture()
	for _, tc := range []struct {
		name     string
		status   int
		content  string
		body     string
		notFound bool
	}{
		{"route missing", 404, "text/plain", "404 page not found", false},
		{"generic 404", 404, "application/problem+json", `{"error":"not_found"}`, false},
		{"exact absence", 404, "application/problem+json", `{"type":"https://norn.dev/problems/offline_effect_not_found","title":"Not Found","status":404,"code":"offline_effect_not_found","detail":"signed effect state is absent or inconsistent","error":"signed effect state is absent or inconsistent"}` + "\n", true},
		{"exact absence with request ID", 404, "application/problem+json", `{"type":"https://norn.dev/problems/offline_effect_not_found","title":"Not Found","status":404,"code":"offline_effect_not_found","detail":"signed effect state is absent or inconsistent","requestId":"request-17","error":"signed effect state is absent or inconsistent"}` + "\n", true},
		{"wrong detail", 404, "application/problem+json", `{"type":"https://norn.dev/problems/offline_effect_not_found","title":"Not Found","status":404,"code":"offline_effect_not_found","detail":"not the target ledger","error":"not the target ledger"}` + "\n", false},
		{"wrong error", 404, "application/problem+json", `{"type":"https://norn.dev/problems/offline_effect_not_found","title":"Not Found","status":404,"code":"offline_effect_not_found","detail":"signed effect state is absent or inconsistent","error":"different cause"}` + "\n", false},
		{"missing detail", 404, "application/problem+json", `{"type":"https://norn.dev/problems/offline_effect_not_found","title":"Not Found","status":404,"code":"offline_effect_not_found","error":"signed effect state is absent or inconsistent"}` + "\n", false},
		{"duplicate key", 200, "application/json", `{"challenge":{},"challenge":{}}`, false},
		{"unexpected field", 200, "application/json", `{"challenge":{},"other":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal, _ := testHTTPClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.content)
				w.Header().Set("Cache-Control", "private, no-store")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			c := &EffectHTTPClient{terminal: terminal, paths: make(map[string]string)}
			_, err := c.ReadEffectChallenge(context.Background(), testPlanID, testAttemptID, offline.EffectIssuePurpose)
			if errors.Is(err, ErrNotFound) != tc.notFound || (!tc.notFound && err == nil) {
				t.Fatalf("absence classification: %v", err)
			}
		})
	}
	terminal, _ := testHTTPClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		receipt.AttestationSHA256 = strings.Repeat("0", 64)
		_ = json.NewEncoder(w).Encode(struct {
			Accepted etcdstore.AcceptedOfflineEffectAttestation `json:"accepted"`
		}{receipt})
	}))
	c := &EffectHTTPClient{terminal: terminal, paths: map[string]string{effectReceiptKey(testPlanID, testAttemptID, challenge.ID): offline.EffectIssuePurpose}}
	if _, err := c.ReadAcceptedEffectAttestation(context.Background(), testPlanID, testAttemptID, challenge.ID); err == nil {
		t.Fatal("tampered accepted receipt")
	}
}

func TestEffectHTTPClientRequiresDedicatedClonedCA(t *testing.T) {
	if _, err := NewEffectHTTPClient(HTTPConfig{ServerName: "fleet.test", Address: "127.0.0.1:443", ObserverBearer: "observer-secret"}); err == nil {
		t.Fatal("effect client inherited system roots")
	}
	_, server := testHTTPClient(t, http.NotFoundHandler())
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	client, err := NewEffectHTTPClient(HTTPConfig{ServerName: "fleet.test", Address: server.Listener.Addr().String(),
		RootCAs: pool, ObserverBearer: "observer-secret"})
	if err != nil {
		t.Fatal(err)
	}
	actual := client.terminal.client.Transport.(*http.Transport).TLSClientConfig.RootCAs
	if actual == nil || actual == pool || len(actual.Subjects()) != len(pool.Subjects()) {
		t.Fatal("dedicated CA pool was not cloned into TLS transport")
	}
}

func TestEffectHTTPClientDoesNotRetryAmbiguousPUT(t *testing.T) {
	_, signed, _ := effectHTTPFixture()
	puts := 0
	terminal, _ := testHTTPClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		puts++
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"https://norn.dev/problems/offline_effect_not_found","title":"Not Found","status":404,"code":"offline_effect_not_found","detail":"signed effect state is absent or inconsistent","error":"signed effect state is absent or inconsistent"}`))
	}))
	c := &EffectHTTPClient{terminal: terminal, paths: map[string]string{effectReceiptKey(testPlanID, testAttemptID, testEffectChallengeID): offline.EffectIssuePurpose}}
	if err := c.SubmitEffectAttestation(context.Background(), testPlanID, testAttemptID, signed); err == nil || errors.Is(err, ErrNotFound) || puts != 1 {
		t.Fatalf("PUT retry or false absence: err=%v puts=%d", err, puts)
	}
}
