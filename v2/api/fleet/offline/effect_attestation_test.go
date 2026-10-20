package offline

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func effectAttestationFixture(t *testing.T, purpose string) (EffectChallengeV1, Binding, Attempt, *EffectGrant, SignedEffectAttestationV1, ed25519.PublicKey, time.Time) {
	t.Helper()
	b, a, g, now := deleteInputs(t)
	var grant *EffectGrant
	if purpose == EffectConsumePurpose {
		g.Phase = "issued"
		g.ConsumedAt = nil
		grant = &g
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewEffectChallengeV1(purpose, b, a, 2, "management-observer", "observer-key-1", 7, public, bytes.Repeat([]byte{0x71}, 32), grant, now)
	if err != nil {
		t.Fatal(err)
	}
	observed := now.Add(3 * time.Second)
	o := EffectObservation{PlanID: b.PlanID, AttemptID: a.ID, BindingSHA256: a.BindingSHA256, TargetID: b.TargetID,
		SourceCommit: b.SourceCommit, SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256, Backend: b.Backend,
		ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName, DropletID: b.DropletID,
		FenceGeneration: b.FenceGeneration, AuthorityEpoch: 2, ObservedAt: observed, EvidenceSHA256: strings.Repeat("a", 64),
		TrustedOrigin: true, ProviderExact: true, LatestBackendExact: true, AllNamespacesInspected: true, App03Empty: true,
		SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}
	att, err := NewEffectAttestationV1(c, o, now.Add(time.Second), strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	s, err := SignEffectAttestationV1(att, "observer-key-1", private)
	if err != nil {
		t.Fatal(err)
	}
	return c, b, a, grant, s, public, observed.Add(time.Second)
}

func TestEffectAttestationExactIssueAndConsume(t *testing.T) {
	for _, purpose := range []string{EffectIssuePurpose, EffectConsumePurpose} {
		t.Run(purpose, func(t *testing.T) {
			c, b, a, g, s, pub, now := effectAttestationFixture(t, purpose)
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeEffectChallengeV1(raw)
			if err != nil || decoded != c {
				t.Fatalf("challenge: %v", err)
			}
			body, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			signed, err := DecodeSignedEffectAttestationV1(body)
			if err != nil {
				t.Fatal(err)
			}
			sha, err := VerifyEffectAttestationV1(signed, c, b, a, g, 2, "management-observer", "observer-key-1", pub, now)
			if err != nil || !hex64.MatchString(sha) {
				t.Fatalf("verify: %v", err)
			}
			for _, invalid := range [][]byte{append([]byte(" "), body...), bytes.Replace(body, []byte(`"keyId":`), []byte(`"keyId":"duplicate","keyId":`), 1), bytes.Replace(body, []byte(`"keyId":`), []byte(`"extra":1,"keyId":`), 1)} {
				if _, err := DecodeSignedEffectAttestationV1(invalid); err == nil {
					t.Fatal("accepted noncanonical envelope")
				}
			}
		})
	}
}

func TestEffectAttestationRejectsPurposeTamperReplayAndStaleReads(t *testing.T) {
	c, b, a, g, s, pub, now := effectAttestationFixture(t, EffectConsumePurpose)
	check := func(name string, envelope SignedEffectAttestationV1, challenge EffectChallengeV1, grant *EffectGrant, at time.Time) {
		t.Helper()
		if _, err := VerifyEffectAttestationV1(envelope, challenge, b, a, grant, 2, "management-observer", "observer-key-1", pub, at); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	changed := s
	changed.Attestation.ProviderEvidenceSHA256 = strings.Repeat("9", 64)
	check("digest tamper", changed, c, g, now)
	changed = s
	changed.Attestation.Purpose = EffectIssuePurpose
	check("purpose confusion", changed, c, g, now)
	changed = s
	changed.Attestation.Observation.Backend.VersionID = "other"
	check("backend read mismatch", changed, c, g, now)
	changed = s
	changed.Attestation.Observation.ServicePassing = false
	check("unsafe workload", changed, c, g, now)
	changedGrant := *g
	changedGrant.PrechangeSHA256 = strings.Repeat("9", 64)
	check("changed issued grant", s, c, &changedGrant, now)
	changedChallenge := c
	changedChallenge.Nonce = strings.Repeat("x", 43)
	check("challenge replay", s, changedChallenge, g, now)
	check("stale consume", s, c, g, now.Add(11*time.Second))
	check("expired challenge", s, c, g, c.ExpiresAt)
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEffectAttestationV1(s, c, b, a, g, 2, "management-observer", "observer-key-1", otherPub, now); err == nil {
		t.Fatal("accepted unpinned key")
	}
	terminal := TerminalAttestationV1{SchemaVersion: TerminalAttestationSchemaV1, Purpose: TerminalChallengePurpose}
	if _, err := SignEffectAttestationV1(EffectAttestationV1{SchemaVersion: terminal.SchemaVersion, Purpose: terminal.Purpose}, "observer-key-1", ed25519.PrivateKey(make([]byte, ed25519.PrivateKeySize))); err == nil {
		t.Fatal("accepted terminal schema")
	}
}

func TestEffectChallengeRejectsFabricatedAttemptAndBinding(t *testing.T) {
	c, b, a, _, _, pub, now := effectAttestationFixture(t, EffectIssuePurpose)
	for name, change := range map[string]func(*Binding, *Attempt){
		"unrelated binding":        func(b *Binding, _ *Attempt) { b.CandidateSHA256 = strings.Repeat("f", 64) },
		"fabricated digest":        func(_ *Binding, a *Attempt) { a.BindingSHA256 = strings.Repeat("f", 64) },
		"wrong attempt schema":     func(_ *Binding, a *Attempt) { a.SchemaVersion = "invented" },
		"wrong attempt status":     func(_ *Binding, a *Attempt) { a.Status = "succeeded" },
		"missing runner device":    func(_ *Binding, a *Attempt) { a.PrincipalDeviceID = "" },
		"attempt predates binding": func(b *Binding, a *Attempt) { a.CreatedAt = b.CreatedAt.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			bb, aa := b, a
			change(&bb, &aa)
			if c.Validate(bb, aa, 2, now) != ErrEffectAttestation {
				t.Fatal("accepted bad provenance")
			}
		})
	}
	bad := a
	bad.BindingSHA256 = strings.Repeat("f", 64)
	if _, err := NewEffectChallengeV1(EffectIssuePurpose, b, bad, 2, "management-observer", "observer-key-1", 7, pub, bytes.Repeat([]byte{0x71}, 32), nil, now); err == nil {
		t.Fatal("constructed challenge for fabricated attempt")
	}
}

func TestEffectConsumeRejectsInvalidGrantTimeProvenance(t *testing.T) {
	c, b, a, g, s, pub, now := effectAttestationFixture(t, EffectConsumePurpose)
	for name, change := range map[string]func(*EffectGrant){
		"zero issue":     func(g *EffectGrant) { g.IssuedAt = time.Time{} },
		"future issue":   func(g *EffectGrant) { g.IssuedAt = now.Add(time.Second) },
		"early issue":    func(g *EffectGrant) { g.IssuedAt = a.CreatedAt.Add(-time.Second) },
		"long lifetime":  func(g *EffectGrant) { g.ExpiresAt = g.IssuedAt.Add(61 * time.Second) },
		"consumed phase": func(g *EffectGrant) { g.Phase = "consumed" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *g
			change(&changed)
			if _, err := VerifyEffectAttestationV1(s, c, b, a, &changed, 2, "management-observer", "observer-key-1", pub, now); err == nil {
				t.Fatal("accepted invalid issued grant")
			}
			if _, err := NewEffectChallengeV1(EffectConsumePurpose, b, a, 2, "management-observer", "observer-key-1", 7, pub, bytes.Repeat([]byte{0x71}, 32), &changed, c.IssuedAt); err == nil {
				t.Fatal("constructed from invalid issued grant")
			}
		})
	}
}
