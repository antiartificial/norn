package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
)

type reservationHTTPStore struct{ saved offline.Binding }

func (s *reservationHTTPStore) ReserveOfflineDispatchWithRunnerProof(_ context.Context, b offline.Binding, _ offline.VerifiedApproval, _ etcdstore.OfflineRunnerProof, _ time.Time) (offline.Binding, error) {
	s.saved = b
	return b, nil
}
func (s *reservationHTTPStore) GetOfflineDispatchReservation(_ context.Context, id string) (offline.Binding, error) {
	if s.saved.PlanID != id {
		return offline.Binding{}, etcdstore.ErrNotFound
	}
	return s.saved, nil
}

type reservationEvidenceVerifier struct {
	err   error
	calls int
}

type reservationDeviceStore struct {
	proof etcdstore.OfflineRunnerProof
	err   error
	calls int
}

func (s *reservationDeviceStore) ProveOfflineRunner(_ context.Context, subject string, _ time.Time) (etcdstore.OfflineRunnerProof, error) {
	s.calls++
	if s.err != nil || s.proof.Subject != subject {
		return etcdstore.OfflineRunnerProof{}, errors.New("device not found")
	}
	return s.proof, nil
}

func reservationDevices(b offline.Binding) *reservationDeviceStore {
	id := strings.TrimPrefix(b.RunnerPrincipalSubject, "fleet-offline-device:")
	return &reservationDeviceStore{proof: etcdstore.OfflineRunnerProof{DeviceID: id, TokenID: uuid.NewString(), Subject: b.RunnerPrincipalSubject,
		DeviceRevision: 2, TokenRevision: 3, TokenExpiresAt: time.Now().Add(time.Hour)}}
}

func (v *reservationEvidenceVerifier) VerifyOfflineReservation(_ context.Context, _ offline.Binding) error {
	v.calls++
	return v.err
}

func signedReservation(t *testing.T, now time.Time) (offline.Binding, offline.SignedApproval, ed25519.PublicKey) {
	return signedReservationSubject(t, now, "fleet-offline-device:"+uuid.NewString())
}

func signedReservationSubject(t *testing.T, now time.Time, subject string) (offline.Binding, offline.SignedApproval, ed25519.PublicKey) {
	t.Helper()
	private := ed25519.NewKeyFromSeed([]byte("01234567890123456789012345678901"))
	b := offline.Binding{SchemaVersion: offline.BindingSchema, PlanID: uuid.NewString(), TargetID: "tgt_01",
		Resource: offline.WorkResource, SourceCommit: strings.Repeat("a", 40), SavedPlanSHA256: strings.Repeat("b", 64),
		EffectSHA256: strings.Repeat("c", 64), Backend: offline.Backend{Bucket: "work-state", Key: "staging/terraform.tfstate", VersionID: "v7", Lineage: "lineage", Serial: 22, RawSHA256: strings.Repeat("d", 64)},
		ProviderAccountUUID: "account", NodeName: offline.WorkNode, DropletID: offline.WorkDropletID,
		RunnerPrincipalSubject: subject, DispatchNonceSHA256: strings.Repeat("f", 64), FenceGeneration: 1,
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute)}
	authID := sha256.Sum256([]byte(b.PlanID))
	b.OwnerAuthorizationID = hex.EncodeToString(authID[:])
	intent := offline.ApprovalIntent{SchemaVersion: offline.ApprovalSchema, AuthorizationID: b.OwnerAuthorizationID, AllowDestructive: true,
		PlanID: b.PlanID, TargetID: b.TargetID, Resource: b.Resource, SourceCommit: b.SourceCommit, SavedPlanSHA256: b.SavedPlanSHA256,
		EffectSHA256: b.EffectSHA256, Backend: b.Backend, ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName,
		DropletID: b.DropletID, RunnerPrincipalSubject: b.RunnerPrincipalSubject, DispatchNonceSHA256: b.DispatchNonceSHA256,
		FenceGeneration: b.FenceGeneration, ExpiresAt: b.ExpiresAt}
	intentRaw, _ := json.Marshal(intent)
	signed := offline.SignedApproval{Intent: intent, KeyID: "owner-2026", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, intentRaw))}
	envelope, _ := json.Marshal(signed)
	digest := sha256.Sum256(envelope)
	b.OwnerEnvelopeSHA256 = hex.EncodeToString(digest[:])
	return b, signed, private.Public().(ed25519.PublicKey)
}

func reservationBody(t *testing.T, b offline.Binding, a offline.SignedApproval) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"binding": b, "approval": a})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOfflineReservationHTTPRequiresVerifierAndExactOwnerSignature(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	b, approval, public := signedReservation(t, now)
	store := &reservationHTTPStore{}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/offline/plans/{planID}/reservation", etcdOfflineReservationCreate(store, reservationDevices(b), nil, "owner-2026", public))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/plans/"+b.PlanID+"/reservation", strings.NewReader(string(reservationBody(t, b, approval)))))
	if rec.Code != http.StatusServiceUnavailable || store.saved.PlanID != "" {
		t.Fatalf("without verifier = %d, saved=%q", rec.Code, store.saved.PlanID)
	}

	verifier := &reservationEvidenceVerifier{}
	router = chi.NewRouter()
	router.Post("/api/v1/fleet/offline/plans/{planID}/reservation", etcdOfflineReservationCreate(store, reservationDevices(b), verifier, "owner-2026", public))
	changed := b
	changed.EffectSHA256 = strings.Repeat("1", 64)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/plans/"+b.PlanID+"/reservation", strings.NewReader(string(reservationBody(t, changed, approval)))))
	if rec.Code != http.StatusForbidden || verifier.calls != 0 {
		t.Fatalf("changed signed binding = %d, verifier calls=%d", rec.Code, verifier.calls)
	}
}

func TestOfflineReservationHTTPCreateAndReadbackNoStore(t *testing.T) {
	b, approval, public := signedReservation(t, time.Now().UTC().Truncate(time.Second))
	store, verifier := &reservationHTTPStore{}, &reservationEvidenceVerifier{}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/offline/plans/{planID}/reservation", etcdOfflineReservationCreate(store, reservationDevices(b), verifier, "owner-2026", public))
	router.Get("/api/v1/fleet/offline/plans/{planID}/reservation", etcdOfflineReservationGet(store))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/plans/"+b.PlanID+"/reservation", strings.NewReader(string(reservationBody(t, b, approval)))))
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "private, no-store" || verifier.calls != 1 {
		t.Fatalf("create = %d cache=%q calls=%d body=%s", rec.Code, rec.Header().Get("Cache-Control"), verifier.calls, rec.Body)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/offline/plans/"+b.PlanID+"/reservation", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("get = %d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestHTTPOfflineEvidenceVerifierComparesAuthenticatedEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	b, _, _ := signedReservation(t, now)
	evidenceBinding := b
	var token string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(offlineEvidence{SchemaVersion: "norn.fleet-offline-independent-evidence/v1", PlanID: evidenceBinding.PlanID, Resource: evidenceBinding.Resource, SourceCommit: evidenceBinding.SourceCommit, SavedPlanSHA256: evidenceBinding.SavedPlanSHA256, EffectSHA256: evidenceBinding.EffectSHA256, Backend: evidenceBinding.Backend, ProviderAccountUUID: evidenceBinding.ProviderAccountUUID, NodeName: evidenceBinding.NodeName, DropletID: evidenceBinding.DropletID, ObservedAt: now})
	}))
	defer server.Close()
	tokenFile := t.TempDir() + "/token"
	if err := os.WriteFile(tokenFile, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	verifier, err := newHTTPOfflineEvidenceVerifier(server.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return now }
	if err := verifier.VerifyOfflineReservation(context.Background(), b); err != nil || token != "Bearer secret" {
		t.Fatalf("verify=%v token=%q", err, token)
	}
	b.DropletID = "610327216"
	if err := verifier.VerifyOfflineReservation(context.Background(), b); err == nil {
		t.Fatal("changed node-to-droplet identity was accepted")
	}
}

func TestOfflineReservationEvidenceFailureDoesNotReachStore(t *testing.T) {
	b, approval, public := signedReservation(t, time.Now().UTC().Truncate(time.Second))
	store := &reservationHTTPStore{}
	verifier := &reservationEvidenceVerifier{err: errors.New("no backend proof")}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/offline/plans/{planID}/reservation", etcdOfflineReservationCreate(store, reservationDevices(b), verifier, "owner-2026", public))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/plans/"+b.PlanID+"/reservation", strings.NewReader(string(reservationBody(t, b, approval)))))
	if rec.Code != http.StatusPreconditionFailed || store.saved.PlanID != "" {
		t.Fatalf("failure=%d saved=%q", rec.Code, store.saved.PlanID)
	}
}

func TestOfflineReservationRequiresLiveEnrolledRunnerSubject(t *testing.T) {
	b, approval, public := signedReservation(t, time.Now().UTC().Truncate(time.Second))
	devices := reservationDevices(b)
	revoked := time.Now().UTC()
	_ = revoked
	devices.err = errors.New("revoked")
	store, verifier := &reservationHTTPStore{}, &reservationEvidenceVerifier{}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/offline/plans/{planID}/reservation", etcdOfflineReservationCreate(store, devices, verifier, "owner-2026", public))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/fleet/offline/plans/"+b.PlanID+"/reservation", strings.NewReader(string(reservationBody(t, b, approval)))))
	if rec.Code != http.StatusPreconditionFailed || verifier.calls != 0 || store.saved.PlanID != "" {
		t.Fatalf("revoked runner = %d verifier=%d saved=%q", rec.Code, verifier.calls, store.saved.PlanID)
	}
}

func TestOfflineReservationRuntimeIsDisabledOrFailsClosed(t *testing.T) {
	key, verifier, err := newOfflineReservationRuntime(&config.Config{})
	if err != nil || key != nil || verifier != nil {
		t.Fatalf("disabled runtime = key %v verifier %v err %v", key, verifier, err)
	}
	if _, _, err := newOfflineReservationRuntime(&config.Config{FleetOfflineReservationEnabled: true}); err == nil {
		t.Fatal("enabled runtime without pinned authority and verifier was accepted")
	}
}

func TestOfflineReservationCapabilityIsConditional(t *testing.T) {
	disabled := etcdFleetCapabilities(&config.Config{}, false)
	if _, ok := disabled["endpoints"].(map[string]string)["fleetOfflineReservation"]; ok {
		t.Fatal("disabled route advertised")
	}
	enabled := etcdFleetCapabilities(&config.Config{FleetOfflineReservationEnabled: true}, false)
	if enabled["endpoints"].(map[string]string)["fleetOfflineReservation"] == "" {
		t.Fatal("enabled route not advertised")
	}
}
