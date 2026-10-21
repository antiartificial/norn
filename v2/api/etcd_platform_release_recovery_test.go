package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/config"

	"norn/v2/api/etcdstore"
)

func TestPlatformRecoveryRouteRequiresIndependentGate(t *testing.T) {
	router := chi.NewRouter()
	registerEtcdPlatformReleaseRecoveryRoutes(router, &config.Config{FleetPlatformReleaseIntentEnabled: true, FleetPlatformRecoverySupervisorPublicKeyFile: "unused", FleetPlatformRecoverySupervisorID: "supervisor"}, nil, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/fleet/platform-releases/staging/attempt/recover", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("recovery route mounted without its independent gate: %d", recorder.Code)
	}
}

func TestVerifyPlatformRecoverySupervisorFixture(t *testing.T) {
	directory := os.Getenv("NORN_TEST_RECOVERY_FIXTURE_DIR")
	if directory == "" {
		t.Skip("NORN_TEST_RECOVERY_FIXTURE_DIR is not set")
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	key, keyHash, err := platformGenesisPublicKey(filepath.Join(directory, "attestation.pub.pem"))
	if err != nil {
		t.Fatal(err)
	}
	raw, signature := read("attestation.json"), read("attestation.sig")
	var att platformRecoveryStopAttestation
	if err := json.Unmarshal(raw, &att); err != nil {
		t.Fatal(err)
	}
	intent := &etcdstore.PlatformReleaseIntent{Name: att.IntentName, IntentDigest: att.IntentDigest, OldSourceSHA: strings.Repeat("a", 40), NewSourceSHA: strings.Repeat("b", 40)}
	for _, member := range att.MemberObservations {
		intent.ControlRoster = append(intent.ControlRoster, etcdstore.PlatformReleaseControl{Name: member.Name})
	}
	attempt := &etcdstore.PlatformReleaseAttempt{ID: att.AttemptID, IntentDigest: att.IntentDigest, FenceEpoch: att.OldFenceEpoch, OwnerID: att.OldOwnerID, OwnerSession: att.OldOwnerSession, LeaseExpiresAt: att.OldLeaseExpiresAt, Status: "Running"}
	now := time.Date(2026, 10, 21, 12, 1, 0, 0, time.UTC)
	verified, err := verifyPlatformRecoveryStopAttestation(raw, signature, key, keyHash, att.SupervisorID, intent, attempt, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.SupervisorPublicKeySHA256 != keyHash || verified.RecoveryNonce != att.RecoveryNonce {
		t.Fatalf("unexpected verifier output: %+v", verified)
	}
	if _, err := verifyPlatformRecoveryStopAttestation(raw, signature, key, keyHash, att.SupervisorID, intent, attempt, nil, now.Add(11*time.Minute)); err == nil {
		t.Fatal("accepted stale observation")
	}
	wrong := *attempt
	wrong.FenceEpoch++
	if _, err := verifyPlatformRecoveryStopAttestation(raw, signature, key, keyHash, att.SupervisorID, intent, &wrong, nil, now); err == nil {
		t.Fatal("accepted wrong fence epoch")
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-2] = '0'
	if _, err := verifyPlatformRecoveryStopAttestation(tampered, signature, key, keyHash, att.SupervisorID, intent, attempt, nil, now); err == nil {
		t.Fatal("accepted tampered signature payload")
	}
	mixedRaw, mixedSignature := read("attestation-mixed.json"), read("attestation-mixed.sig")
	if _, err := verifyPlatformRecoveryStopAttestation(mixedRaw, mixedSignature, key, keyHash, att.SupervisorID, intent, attempt, nil, now); err == nil {
		t.Fatal("accepted mixed member without durable promote begin")
	}
	checkpoints := []etcdstore.PlatformReleaseCheckpoint{{Ordinal: 1, Phase: "promote:begin", ControlName: "control-01"}}
	if _, err := verifyPlatformRecoveryStopAttestation(mixedRaw, mixedSignature, key, keyHash, att.SupervisorID, intent, attempt, checkpoints, now); err != nil {
		t.Fatalf("rejected signed interrupted promote with open begin: %v", err)
	}
	completed := append(checkpoints, etcdstore.PlatformReleaseCheckpoint{Ordinal: 2, Phase: "promote:done", ControlName: "control-01"})
	if _, err := verifyPlatformRecoveryStopAttestation(mixedRaw, mixedSignature, key, keyHash, att.SupervisorID, intent, attempt, completed, now); err == nil {
		t.Fatal("accepted mixed member after durable promote done")
	}
}
