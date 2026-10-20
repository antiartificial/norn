package etcdstore

import (
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

func signedConsumeFixture(t *testing.T) (*etcdExecutorHarness, offline.Binding, offline.Attempt, offline.EffectGrant, offline.EffectChallengeV1, []byte, time.Time) {
	t.Helper()
	h, b, a, _ := offlineEffectFixture(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 1, ObserverSubject: "management-observer", KeyID: "observer-key-1", PublicKey: base64.RawURLEncoding.EncodeToString(public)}
	trustRaw, _ := json.Marshal(trust)
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), string(trustRaw)); err != nil {
		t.Fatal(err)
	}
	issueChallenge, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, trust.ObserverSubject)
	if err != nil {
		t.Fatal(err)
	}
	issueObserved := issueChallenge.IssuedAt.Add(time.Millisecond)
	issueObs := offlineEffectObservation(b, a, issueObserved)
	issueAtt, err := offline.NewEffectAttestationV1(issueChallenge, issueObs, issueChallenge.IssuedAt,
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	issueSigned, err := offline.SignEffectAttestationV1(issueAtt, trust.KeyID, private)
	if err != nil {
		t.Fatal(err)
	}
	issueRaw, _ := json.Marshal(issueSigned)
	issueAcceptAt := issueChallenge.IssuedAt.Add(2 * time.Millisecond)
	if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, issueChallenge.ID, issueRaw, func() time.Time { return issueAcceptAt }); err != nil {
		t.Fatal(err)
	}
	grant, err := h.s.issueOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, issueChallenge.ID, offlineEffectIdentity(b, a), func() time.Time { return issueAcceptAt.Add(time.Millisecond) })
	if err != nil {
		t.Fatal(err)
	}
	consumeChallenge, err := h.s.issueOfflineEffectChallengeWithClock(h.ctx(), b.PlanID, a.ID, offline.EffectConsumePurpose, trust.ObserverSubject,
		func() time.Time { return grant.IssuedAt.Add(time.Millisecond) })
	if err != nil {
		t.Fatal(err)
	}
	consumeObserved := consumeChallenge.IssuedAt.Add(time.Millisecond)
	consumeObs := offlineEffectObservation(b, a, consumeObserved)
	consumeAtt, err := offline.NewEffectAttestationV1(consumeChallenge, consumeObs, consumeChallenge.IssuedAt,
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	consumeSigned, err := offline.SignEffectAttestationV1(consumeAtt, trust.KeyID, private)
	if err != nil {
		t.Fatal(err)
	}
	consumeRaw, _ := json.Marshal(consumeSigned)
	acceptAt := consumeChallenge.IssuedAt.Add(2 * time.Millisecond)
	if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, offline.EffectConsumePurpose, consumeChallenge.ID, consumeRaw,
		func() time.Time { return acceptAt }); err != nil {
		t.Fatal(err)
	}
	return h, b, a, grant, consumeChallenge, consumeRaw, acceptAt
}

func TestSignedEffectConsumeOneUseRecoverAndLegacyFence(t *testing.T) {
	h, b, a, grant, c, _, now := signedConsumeFixture(t)
	if _, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, offlineEffectIdentity(b, a), now, offlineBackendReadFor(b)); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("legacy unsigned consume crossed signed challenge: %v", err)
	}
	consumed, err := h.s.consumeOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, grant.ID, c.ID, offlineEffectIdentity(b, a), func() time.Time { return now.Add(time.Millisecond) })
	if err != nil || consumed.Phase != "consumed" || consumed.ConsumedAt == nil {
		t.Fatalf("consume: %+v %v", consumed, err)
	}
	recovered, err := h.s.GetOfflineSignedConsumedGrant(h.ctx(), b.PlanID, a.ID, c.ID)
	if err != nil || recovered.Phase != "consumed" || recovered.ID != grant.ID {
		t.Fatalf("readback: %+v %v", recovered, err)
	}
	replay, err := h.s.consumeOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, grant.ID, c.ID, offlineEffectIdentity(b, a), func() time.Time { return c.ExpiresAt.Add(time.Hour) })
	if err != nil || replay.Phase != "consumed" || !replay.ConsumedAt.Equal(*consumed.ConsumedAt) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	wrong := offlineEffectIdentity(b, a)
	wrong.DeviceID = "different"
	if _, err := h.s.consumeOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, grant.ID, c.ID, wrong, func() time.Time { return c.ExpiresAt.Add(time.Hour) }); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong identity replay: %v", err)
	}
}

func TestSignedEffectConsumeRejectsStaleAcceptedEvidence(t *testing.T) {
	h, b, a, grant, c, _, now := signedConsumeFixture(t)
	if _, err := h.s.consumeOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, grant.ID, c.ID, offlineEffectIdentity(b, a),
		func() time.Time { return now.Add(11 * time.Second) }); err == nil {
		t.Fatal("stale consume evidence accepted")
	}
	current, err := h.s.GetOfflineEffectGrant(h.ctx(), b.PlanID)
	if err != nil || current.Phase != "issued" {
		t.Fatalf("stale evidence consumed grant: %+v %v", current, err)
	}
}

func TestSignedEffectConsumeWatermarkBlocksPartialRollback(t *testing.T) {
	h, b, a, grant, c, _, now := signedConsumeFixture(t)
	if _, err := h.s.consumeOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, grant.ID, c.ID,
		offlineEffectIdentity(b, a), func() time.Time { return now.Add(time.Millisecond) }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineSignedEffectConsumeProofKey(b.PlanID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetOfflineSignedConsumedGrant(h.ctx(), b.PlanID, a.ID, c.ID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("missing proof with surviving watermark: %v", err)
	}
	if _, err := h.s.consumeOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, grant.ID, c.ID,
		offlineEffectIdentity(b, a), func() time.Time { return c.ExpiresAt.Add(time.Hour) }); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("rollback reconsumed grant: %v", err)
	}
}
