package offline

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func signedApprovalFor(t *testing.T, b Binding, key ed25519.PrivateKey) (Binding, SignedApproval) {
	t.Helper()
	schema := ApprovalSchema
	if b.SchemaVersion == BindingSchemaV2 {
		schema = ApprovalSchemaV2
	}
	intent := ApprovalIntent{SchemaVersion: schema, AuthorizationID: strings.Repeat("9", 64), AllowDestructive: true,
		PlanID: b.PlanID, TargetID: b.TargetID, Resource: b.Resource, SourceCommit: b.SourceCommit,
		SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256, Backend: b.Backend,
		CandidateSHA256: b.CandidateSHA256, ProviderNodes: b.ProviderNodes,
		ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName, DropletID: b.DropletID,
		RunnerPrincipalSubject: b.RunnerPrincipalSubject, DispatchNonceSHA256: b.DispatchNonceSHA256,
		FenceGeneration: b.FenceGeneration, ExpiresAt: b.ExpiresAt}
	content, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	a := SignedApproval{Intent: intent, KeyID: "owner-key-1",
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, content))}
	encoded, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	b.OwnerEnvelopeSHA256 = hex.EncodeToString(digest[:])
	return b, a
}

func TestSignedApprovalExactBindingAndPinnedKey(t *testing.T) {
	seed := []byte("01234567890123456789012345678901")
	key := ed25519.NewKeyFromSeed(seed)
	b, a := signedApprovalFor(t, goodBinding(), key)
	now := b.CreatedAt.Add(time.Minute)
	verified, err := VerifySignedApproval(a, a.KeyID, key.Public().(ed25519.PublicKey), b, now)
	if err != nil || !verified.Match(b) || verified.AuthorizationID() != strings.Repeat("9", 64) {
		t.Fatalf("exact signed binding refused: %v", err)
	}
	if (VerifiedApproval{}).Match(b) {
		t.Fatal("zero-value approval matched")
	}
	changedAfterVerification := b
	changedAfterVerification.EffectSHA256 = strings.Repeat("1", 64)
	if verified.Match(changedAfterVerification) {
		t.Fatal("verified token accepted a changed binding")
	}
	wrong := b
	wrong.Backend.RawSHA256 = strings.Repeat("1", 64)
	if _, err := VerifySignedApproval(a, a.KeyID, key.Public().(ed25519.PublicKey), wrong, now); !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("changed backend accepted")
	}
	wrong = b
	wrong.SourceCommit = strings.Repeat("1", 40)
	if _, err := VerifySignedApproval(a, a.KeyID, key.Public().(ed25519.PublicKey), wrong, now); !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("changed source accepted")
	}
	if _, err := VerifySignedApproval(a, "other-owner", key.Public().(ed25519.PublicKey), b, now); !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("wrong key identity accepted")
	}
	other := ed25519.NewKeyFromSeed([]byte("abcdefghijklmnopqrstuvwxyz123456"))
	if _, err := VerifySignedApproval(a, a.KeyID, other.Public().(ed25519.PublicKey), b, now); !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("wrong pinned key accepted")
	}
	if _, err := VerifySignedApproval(a, a.KeyID, key.Public().(ed25519.PublicKey), b, b.ExpiresAt); !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("expired approval accepted")
	}
	a.Intent.AllowDestructive = false
	if _, err := VerifySignedApproval(a, a.KeyID, key.Public().(ed25519.PublicKey), b, now); !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("missing destructive acknowledgement accepted")
	}
}

func TestSignedApprovalStrictDecodeRejectsAmbiguousJSON(t *testing.T) {
	key := ed25519.NewKeyFromSeed([]byte("01234567890123456789012345678901"))
	_, a := signedApprovalFor(t, goodBinding(), key)
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSignedApproval(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"keyId":"owner-key-1"`, `"keyId":"owner-key-1","keyId":"other"`, 1),
		strings.Replace(string(raw), `"allowDestructive":true`, `"allowDestructive":true,"allowDestructive":false`, 1),
		strings.Replace(string(raw), `"keyId":"owner-key-1"`, `"keyId":"owner-key-1","extra":1`, 1),
	} {
		if _, err := DecodeSignedApproval([]byte(bad)); !errors.Is(err, ErrInvalidApproval) {
			t.Fatal("ambiguous owner envelope accepted")
		}
	}
}
