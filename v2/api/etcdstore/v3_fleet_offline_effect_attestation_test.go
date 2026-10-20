package etcdstore

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

func signedEffectIssueFixture(t *testing.T) (*etcdExecutorHarness, offline.Binding, offline.Attempt, offline.EffectChallengeV1, []byte, time.Time) {
	t.Helper()
	h, b, a, _ := offlineEffectFixture(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 1, ObserverSubject: "management-observer", KeyID: "observer-key-1", PublicKey: base64.RawURLEncoding.EncodeToString(public)}
	raw, _ := json.Marshal(trust)
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), string(raw)); err != nil {
		t.Fatal(err)
	}
	c, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, trust.ObserverSubject)
	if err != nil {
		t.Fatal(err)
	}
	observed := c.IssuedAt.Add(time.Millisecond)
	o := offlineEffectObservation(b, a, observed)
	att, err := offline.NewEffectAttestationV1(c, o, c.IssuedAt, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := offline.SignEffectAttestationV1(att, trust.KeyID, private)
	if err != nil {
		t.Fatal(err)
	}
	envelope, _ := json.Marshal(signed)
	return h, b, a, c, envelope, c.IssuedAt.Add(2 * time.Millisecond)
}

func TestOfflineEffectAttestationAcceptRecoverAndOneUse(t *testing.T) {
	h, b, a, c, envelope, now := signedEffectIssueFixture(t)
	receipt, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if receipt.AttestationSHA256 == "" || receipt.Purpose != offline.EffectIssuePurpose || receipt.Grant != nil || !bytes.Equal(receipt.Envelope, envelope) {
		t.Fatalf("receipt incomplete: %+v", receipt)
	}
	got, err := h.s.ReadAcceptedOfflineEffectAttestation(h.ctx(), b.PlanID, c.Purpose, c.ID)
	if err != nil || !bytes.Equal(got.Envelope, envelope) || got.AcceptedAt != receipt.AcceptedAt {
		t.Fatalf("readback: %+v %v", got, err)
	}
	// Exact retry succeeds after the challenge expires, without a new submit.
	retried, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope, func() time.Time { return c.ExpiresAt.Add(time.Minute) })
	if err != nil || retried.AttestationSHA256 != receipt.AttestationSHA256 {
		t.Fatalf("retry: %+v %v", retried, err)
	}
	different := append([]byte(nil), envelope...)
	different[len(different)-1] = 'x'
	if _, err := h.s.SubmitOfflineEffectAttestation(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, different); err == nil {
		t.Fatal("different signed envelope accepted")
	}
}

func TestOfflineEffectAttestationPartialRestoreFailsClosed(t *testing.T) {
	h, b, a, c, envelope, now := signedEffectIssueFixture(t)
	if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineEffectAttestationEventKey(b.PlanID, c.Purpose)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ReadAcceptedOfflineEffectAttestation(h.ctx(), b.PlanID, c.Purpose, c.ID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("partial restore accepted: %v", err)
	}
	if _, err := h.s.SubmitOfflineEffectAttestation(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("resubmission after partial restore: %v", err)
	}
}

func TestOfflineEffectChallengeHistoricalReadAfterTransitions(t *testing.T) {
	h, b, a, c, envelope, now := signedEffectIssueFixture(t)
	if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineEffectGrantKey(b.PlanID), "transitioned"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetTargetFenceKey(b.TargetID)); err != nil {
		t.Fatal(err)
	}
	rotated := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 2, ObserverSubject: "new-observer", KeyID: "new-key", PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}
	raw, _ := json.Marshal(rotated)
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), string(raw)); err != nil {
		t.Fatal(err)
	}
	recovered, err := h.s.GetOfflineEffectChallenge(h.ctx(), b.PlanID, c.Purpose)
	if err != nil || recovered != c {
		t.Fatalf("historical challenge: %+v %v", recovered, err)
	}
	receipt, err := h.s.ReadAcceptedOfflineEffectAttestation(h.ctx(), b.PlanID, c.Purpose, c.ID)
	if err != nil || !bytes.Equal(receipt.Envelope, envelope) {
		t.Fatalf("historical accepted receipt: %+v %v", receipt, err)
	}
	if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope, func() time.Time { return c.ExpiresAt.Add(time.Minute) }); err != nil {
		t.Fatalf("exact historical recovery: %v", err)
	}
}

func TestOfflineEffectConsumeAcceptanceDoesNotConsumeGrant(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	observation := offlineEffectObservation(b, a, now)
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, offlineEffectIdentity(b, a), now,
		offlineTestReobserver{observation: observation})
	if err != nil {
		t.Fatal(err)
	}
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
	challenge, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectConsumePurpose, trust.ObserverSubject)
	if err != nil {
		t.Fatal(err)
	}
	observed := challenge.IssuedAt.Add(time.Millisecond)
	claim := offlineEffectObservation(b, a, observed)
	att, err := offline.NewEffectAttestationV1(challenge, claim, challenge.IssuedAt,
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := offline.SignEffectAttestationV1(att, trust.KeyID, private)
	if err != nil {
		t.Fatal(err)
	}
	envelope, _ := json.Marshal(signed)
	receipt, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, challenge.Purpose, challenge.ID, envelope,
		func() time.Time { return challenge.IssuedAt.Add(2 * time.Millisecond) })
	if err != nil || receipt.Grant == nil || receipt.Grant.ID != grant.ID {
		t.Fatalf("consume attestation: %+v %v", receipt, err)
	}
	unchanged, err := h.s.GetOfflineEffectGrant(h.ctx(), b.PlanID)
	if err != nil || unchanged.Phase != "issued" || unchanged.ID != grant.ID {
		t.Fatalf("acceptance consumed grant: %+v %v", unchanged, err)
	}
	// Simulate the later one-use transition. Historical challenge and signed
	// acceptance stay readable without reopening the now-consumed grant.
	consumed := grant
	stamp := challenge.IssuedAt.Add(3 * time.Millisecond)
	consumed.Phase, consumed.ConsumedAt = "consumed", &stamp
	consumedRaw, _ := json.Marshal(consumed)
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineEffectGrantKey(b.PlanID), string(consumedRaw)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineEffectGrantEventKey(b.PlanID, 2), string(consumedRaw)); err != nil {
		t.Fatal(err)
	}
	if got, err := h.s.GetOfflineEffectChallenge(h.ctx(), b.PlanID, challenge.Purpose); err != nil || got != challenge {
		t.Fatalf("historical consume challenge: %+v %v", got, err)
	}
	if got, err := h.s.ReadAcceptedOfflineEffectAttestation(h.ctx(), b.PlanID, challenge.Purpose, challenge.ID); err != nil || got.AttestationSHA256 != receipt.AttestationSHA256 {
		t.Fatalf("historical consume receipt: %+v %v", got, err)
	}
}

func TestOfflineEffectAttestationRejectsRotatedTrustOrReleasedFence(t *testing.T) {
	for _, kind := range []string{"trust", "fence"} {
		t.Run(kind, func(t *testing.T) {
			h, b, a, c, envelope, now := signedEffectIssueFixture(t)
			if kind == "trust" {
				rotated := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 2,
					ObserverSubject: "management-observer", KeyID: "observer-key-2",
					PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}
				raw, _ := json.Marshal(rotated)
				if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), string(raw)); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetTargetFenceKey(b.TargetID)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope,
				func() time.Time { return now }); err == nil {
				t.Fatal("stale authority accepted signed evidence")
			}
			if got, err := h.s.GetOfflineEffectChallenge(h.ctx(), b.PlanID, c.Purpose); err != nil || got != c {
				t.Fatalf("historical issue lost: %+v %v", got, err)
			}
		})
	}
}
