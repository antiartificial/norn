package offline

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func terminalAttestationFixture(t *testing.T) (TerminalChallengeV1, EffectGrant, TerminalAttestationV1, ed25519.PrivateKey, time.Time) {
	t.Helper()
	b, _, g, now := deleteInputs(t)
	nonce := bytes.Repeat([]byte{0x7d}, 32)
	c, err := NewTerminalChallengeV1(g, "verifier-device", "management-observer", b.DispatchNonceSHA256,
		"44444444-4444-4444-8444-444444444444", nonce, now)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := now.Add(5 * time.Second)
	o := TerminalObservation{
		PlanID: g.PlanID, AttemptID: g.AttemptID, GrantID: g.ID,
		BindingSHA256: g.BindingSHA256, TargetID: g.TargetID,
		FenceGeneration: g.FenceGeneration, AuthorityEpoch: g.AuthorityEpoch,
		ObservedAt: observedAt, EvidenceSHA256: strings.Repeat("1", 64),
		ProviderEvidenceSHA256: strings.Repeat("2", 64),
		BackendEvidenceSHA256:  strings.Repeat("3", 64),
		WorkloadEvidenceSHA256: strings.Repeat("4", 64),
		ProviderAccountUUID:    g.ProviderAccountUUID, NodeName: g.NodeName, DropletID: g.DropletID,
		Backend: Backend{Bucket: g.Backend.Bucket, Key: g.Backend.Key, VersionID: "new-version",
			Lineage: g.Backend.Lineage, Serial: g.Backend.Serial + 1, RawSHA256: strings.Repeat("5", 64)},
		ProviderNodeAbsent: true, BackendLatestExact: true, BackendNodeAbsent: true,
		AllNamespacesInspected: true, WorkloadNodeAbsent: true, SurvivingPlacement: true,
		SurvivingHeadroom: true, ServicePassing: true,
	}
	a, err := NewTerminalAttestationV1(c, o, now.Add(time.Second), strings.Repeat("6", 64))
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, g, a, private, observedAt.Add(time.Second)
}

func TestTerminalAttestationExactSignedRoundTrip(t *testing.T) {
	c, g, a, private, now := terminalAttestationFixture(t)
	envelope, err := SignTerminalAttestationV1(a, "observer-key-1", private)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSignedTerminalAttestationV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	challengeRaw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	decodedChallenge, err := DecodeTerminalChallengeV1(challengeRaw)
	if err != nil || decodedChallenge != c {
		t.Fatalf("challenge round trip: %+v %v", decodedChallenge, err)
	}
	if _, err := DecodeTerminalChallengeV1(append([]byte(" "), challengeRaw...)); err == nil {
		t.Fatal("accepted noncanonical challenge")
	}
	v, err := VerifyTerminalAttestationV1(decoded, c, g, "management-observer", "observer-key-1",
		private.Public().(ed25519.PublicKey), now)
	if err != nil || v.ChallengeID() != c.ID || !hex64.MatchString(v.AttestationSHA256()) {
		t.Fatalf("verification: %+v %v", v, err)
	}
	if _, err := DecodeSignedTerminalAttestationV1(append([]byte(" "), raw...)); err == nil {
		t.Fatal("accepted noncanonical envelope")
	}
	dup := bytes.Replace(raw, []byte(`"keyId":`), []byte(`"keyId":"duplicate","keyId":`), 1)
	if _, err := DecodeSignedTerminalAttestationV1(dup); err == nil {
		t.Fatal("accepted duplicate key")
	}
}

func TestTerminalAttestationRejectsTamperReplayExpiryAndKeyRotation(t *testing.T) {
	c, g, a, private, now := terminalAttestationFixture(t)
	envelope, err := SignTerminalAttestationV1(a, "observer-key-1", private)
	if err != nil {
		t.Fatal(err)
	}
	pub := private.Public().(ed25519.PublicKey)
	verify := func(name string, e SignedTerminalAttestationV1, challenge TerminalChallengeV1, grant EffectGrant,
		subject, keyID string, key ed25519.PublicKey, at time.Time) {
		t.Helper()
		if _, err := VerifyTerminalAttestationV1(e, challenge, grant, subject, keyID, key, at); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	changed := envelope
	changed.Attestation.WorkloadEvidenceSHA256 = strings.Repeat("f", 64)
	verify("unsigned source digest change", changed, c, g, "management-observer", "observer-key-1", pub, now)
	changed = envelope
	changed.Attestation.Observation.ServicePassing = false
	verify("signed but unsafe observation", resignTerminal(t, changed.Attestation, private), c, g, "management-observer", "observer-key-1", pub, now)
	changed = envelope
	changed.Attestation.Purpose = "effect-issue"
	verify("cross-purpose replay", changed, c, g, "management-observer", "observer-key-1", pub, now)
	otherChallenge := c
	otherChallenge.ID = "99999999-9999-4999-8999-999999999999"
	verify("another challenge", envelope, otherChallenge, g, "management-observer", "observer-key-1", pub, now)
	otherGrant := g
	otherGrant.IndependentlyObservedSHA256 = strings.Repeat("f", 64)
	verify("another consumed grant", envelope, c, otherGrant, "management-observer", "observer-key-1", pub, now)
	verify("expired observation", envelope, c, g, "management-observer", "observer-key-1", pub, now.Add(61*time.Second))
	verify("expired challenge", envelope, c, g, "management-observer", "observer-key-1", pub, c.ExpiresAt)
	verify("other subject", envelope, c, g, "other-observer", "observer-key-1", pub, now)
	verify("old key ID after rotation", envelope, c, g, "management-observer", "observer-key-2", pub, now)
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	verify("new public key without resigning", envelope, c, g, "management-observer", "observer-key-1", otherPub, now)
}

func resignTerminal(t *testing.T, a TerminalAttestationV1, private ed25519.PrivateKey) SignedTerminalAttestationV1 {
	t.Helper()
	e, err := SignTerminalAttestationV1(a, "observer-key-1", private)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
