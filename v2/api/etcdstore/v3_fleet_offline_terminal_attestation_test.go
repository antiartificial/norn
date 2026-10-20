package etcdstore

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

func signedTerminalFixture(t *testing.T) (*etcdExecutorHarness, offline.Binding, offline.Attempt, offline.EffectGrant,
	offline.TerminalChallengeV1, []byte, time.Time) {
	t.Helper()
	h, binding, attempt, grant, _ := consumedOfflineGrant(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 1,
		ObserverSubject: "management-observer", KeyID: "observer-key-1",
		PublicKey: base64.RawURLEncoding.EncodeToString(public)}
	trustRaw, _ := json.Marshal(trust)
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), string(trustRaw)); err != nil {
		t.Fatal(err)
	}
	challenge, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
		"fleet-verifier", trust.ObserverSubject)
	if err != nil {
		t.Fatal(err)
	}
	observation := terminalObservation(grant, challenge.IssuedAt.Add(time.Millisecond))
	attestation, err := offline.NewTerminalAttestationV1(challenge, observation, challenge.IssuedAt,
		strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := offline.SignTerminalAttestationV1(attestation, trust.KeyID, private)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(envelope)
	return h, binding, attempt, grant, challenge, raw, challenge.IssuedAt.Add(2 * time.Millisecond)
}

func TestSignedTerminalSubmitRecoverAndFence(t *testing.T) {
	h, binding, attempt, _, challenge, raw, now := signedTerminalFixture(t)
	first, err := h.s.submitOfflineTerminalAttestationWithClock(h.ctx(), binding.PlanID, attempt.ID, challenge.ID, raw,
		func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if first.ChallengeID != challenge.ID || first.ObserverSubject != "management-observer" ||
		first.ObserverKeyID != "observer-key-1" || first.AttestationSHA256 == "" || first.RawJournalSHA256 == "" {
		t.Fatalf("incomplete signed receipt: %+v", first)
	}
	got, err := h.s.GetOfflineTerminal(h.ctx(), binding.PlanID)
	if err != nil || got != first {
		t.Fatalf("restore: %+v %v", got, err)
	}
	// Trust rotation must block new submissions for an old challenge while
	// preserving verification of the already accepted signed receipt.
	rotated := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 2,
		ObserverSubject: "management-observer", KeyID: "observer-key-2",
		PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}
	rotatedRaw, _ := json.Marshal(rotated)
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), string(rotatedRaw)); err != nil {
		t.Fatal(err)
	}
	got, err = h.s.GetOfflineTerminal(h.ctx(), binding.PlanID)
	if err != nil || got != first {
		t.Fatalf("historical signed receipt lost after rotation: %+v %v", got, err)
	}
	// Once committed, replay does not require a still-live challenge.
	replayed, err := h.s.submitOfflineTerminalAttestationWithClock(h.ctx(), binding.PlanID, attempt.ID, challenge.ID, raw,
		func() time.Time { return now.Add(time.Hour) })
	if err != nil || replayed != first {
		t.Fatalf("lost response replay: %+v %v", replayed, err)
	}
	changed := bytes.Clone(raw)
	changed[len(changed)-2] ^= 1
	if _, err := h.s.submitOfflineTerminalAttestationWithClock(h.ctx(), binding.PlanID, attempt.ID, challenge.ID, changed,
		func() time.Time { return now }); err == nil {
		t.Fatal("altered replay accepted")
	}
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), binding.TargetID)
	if err != nil || !fence.Held {
		t.Fatalf("receipt released fence: %+v %v", fence, err)
	}
}

func TestSignedTerminalTamperAndExpired(t *testing.T) {
	h, binding, attempt, _, challenge, raw, now := signedTerminalFixture(t)
	var envelope offline.SignedTerminalAttestationV1
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Attestation.RawJournalSHA256 = strings.Repeat("f", 64)
	tampered, _ := json.Marshal(envelope)
	if _, err := h.s.submitOfflineTerminalAttestationWithClock(h.ctx(), binding.PlanID, attempt.ID, challenge.ID, tampered,
		func() time.Time { return now }); !errors.Is(err, offline.ErrTerminalAttestation) {
		t.Fatalf("signature tamper accepted: %v", err)
	}
	if _, err := h.s.submitOfflineTerminalAttestationWithClock(h.ctx(), binding.PlanID, attempt.ID, challenge.ID, raw,
		func() time.Time { return challenge.ExpiresAt }); !errors.Is(err, offline.ErrTerminalAttestation) {
		t.Fatalf("expired challenge accepted: %v", err)
	}
}

func TestSignedTerminalStaleAuthorityRejected(t *testing.T) {
	for _, kind := range []string{"trust", "epoch", "fence", "alias", "grant", "competing"} {
		t.Run(kind, func(t *testing.T) {
			h, binding, attempt, _, challenge, raw, now := signedTerminalFixture(t)
			var key, value string
			switch kind {
			case "trust":
				trust := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 2,
					ObserverSubject: "management-observer", KeyID: "observer-key-2",
					PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}
				encoded, _ := json.Marshal(trust)
				key, value = h.s.offlineTerminalObserverTrustKey(), string(encoded)
			case "epoch":
				key, value = h.s.fleetAuthorityEpochKey(), "999"
			case "fence":
				key, value = h.s.fleetTargetFenceKey(binding.TargetID), "{}"
			case "alias":
				key, value = h.s.fleetTargetAliasKey("cluster:tk-staging-nyc3"), "different-target"
			case "grant":
				key, value = h.s.offlineEffectGrantKey(binding.PlanID), "{}"
			case "competing":
				key, value = h.s.fleetRunnerAttemptPrefix(binding.PlanID)+"other", "occupied"
			}
			if _, err := h.s.kv.Put(h.ctx(), key, value); err != nil {
				t.Fatal(err)
			}
			if _, err := h.s.submitOfflineTerminalAttestationWithClock(h.ctx(), binding.PlanID, attempt.ID, challenge.ID, raw,
				func() time.Time { return now }); err == nil {
				t.Fatal("stale authority accepted")
			}
		})
	}
}

func TestSignedTerminalConcurrentSubmitOneReceipt(t *testing.T) {
	h, binding, attempt, _, challenge, raw, now := signedTerminalFixture(t)
	var receipts [2]offline.TerminalEvidence
	var errs [2]error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range receipts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			receipts[i], errs[i] = h.s.submitOfflineTerminalAttestationWithClock(h.ctx(), binding.PlanID, attempt.ID,
				challenge.ID, raw, func() time.Time { return now })
		}(i)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || receipts[0] != receipts[1] {
		t.Fatalf("concurrent exact replay: %+v %+v %v %v", receipts[0], receipts[1], errs[0], errs[1])
	}
}

func TestSignedTerminalPartialRestoreRejected(t *testing.T) {
	for _, which := range []string{"receipt", "event", "both-receipt-rows", "consumed", "provenance", "challenge-event"} {
		t.Run(which, func(t *testing.T) {
			h, binding, attempt, _, challenge, raw, now := signedTerminalFixture(t)
			if _, err := h.s.submitOfflineTerminalAttestationWithClock(h.ctx(), binding.PlanID, attempt.ID,
				challenge.ID, raw, func() time.Time { return now }); err != nil {
				t.Fatal(err)
			}
			key := map[string]string{"receipt": h.s.offlineTerminalKey(binding.PlanID),
				"event":             h.s.offlineTerminalEventKey(binding.PlanID),
				"both-receipt-rows": h.s.offlineTerminalKey(binding.PlanID),
				"consumed":          h.s.offlineTerminalChallengeConsumedKey(binding.PlanID),
				"provenance":        h.s.offlineTerminalAttestationProvenanceKey(binding.PlanID),
				"challenge-event":   h.s.offlineTerminalChallengeEventKey(binding.PlanID)}[which]
			if _, err := h.s.kv.Delete(h.ctx(), key); err != nil {
				t.Fatal(err)
			}
			if which == "both-receipt-rows" {
				if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineTerminalEventKey(binding.PlanID)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.s.GetOfflineTerminal(h.ctx(), binding.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
				t.Fatalf("partial restore accepted: %v", err)
			}
		})
	}
}
