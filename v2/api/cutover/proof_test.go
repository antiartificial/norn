package cutover

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"norn/v2/api/database"
)

type acceptanceProofFunc func(context.Context, SignedAcceptedIntent) error

func (f acceptanceProofFunc) VerifyAcceptedIntent(ctx context.Context, a SignedAcceptedIntent) error {
	return f(ctx, a)
}

type readbackFunc func(context.Context, ExternalEffect) error

func (f readbackFunc) VerifyEffect(ctx context.Context, e ExternalEffect) error { return f(ctx, e) }

func TestPhaseProofVerifierRejectsForgedStaleAndCompetingProofs(t *testing.T) {
	j, err := New(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier := PhaseProofVerifier{TrustedObserverKeyID: "observer-1", TrustedObserverKey: pub, Now: func() time.Time { return now }, Acceptance: acceptanceProofFunc(func(_ context.Context, a SignedAcceptedIntent) error {
		if a.Signature.Value != "accepted" {
			return errors.New("untrusted acceptance")
		}
		return nil
	}), Readback: readbackFunc(func(_ context.Context, e ExternalEffect) error {
		if e.ID != "fence-1" {
			return errors.New("unknown effect")
		}
		return nil
	})}
	raw, ref := signedProofBytes(t, j, private, now)
	verified, err := verifier.VerifyPhaseProof(context.Background(), j, ref, raw)
	if err != nil || !verified.Valid() {
		t.Fatalf("verify: %v", err)
	}
	var forgedProof PhaseProof
	if err := json.Unmarshal(raw, &forgedProof); err != nil {
		t.Fatal(err)
	}
	forgedProof.ObserverID = "attacker"
	forged, err := json.Marshal(forgedProof)
	if err != nil {
		t.Fatal(err)
	}
	forgedRef, err := NewPhaseEvidenceReference(j, PhaseQuiesce, digestBytes(forged))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyPhaseProof(context.Background(), j, forgedRef, forged); !errors.Is(err, ErrPhaseProofInvalid) {
		t.Fatalf("forged proof accepted: %v", err)
	}
	for _, next := range []Phase{PhaseFinalSync, PhaseAccept} {
		var skipped PhaseProof
		if err := json.Unmarshal(raw, &skipped); err != nil {
			t.Fatal(err)
		}
		skipped.NextPhase = next
		skippedRaw, skippedRef := resignProof(t, j, private, skipped)
		if _, err := verifier.VerifyPhaseProof(context.Background(), j, skippedRef, skippedRaw); !errors.Is(err, ErrPhaseProofInvalid) {
			t.Fatalf("skipped phase %q accepted: %v", next, err)
		}
	}
	unknownRef := ref
	unknownRef.NextPhase = Phase("unknown")
	if _, err := verifier.VerifyPhaseProof(context.Background(), j, unknownRef, raw); !errors.Is(err, ErrPhaseProofInvalid) {
		t.Fatalf("unknown phase accepted: %v", err)
	}
	staleRaw, staleRef := signedProofBytes(t, j, private, now.Add(-2*time.Hour))
	var stale PhaseProof
	if err := json.Unmarshal(staleRaw, &stale); err != nil {
		t.Fatal(err)
	}
	stale.ExpiresAt = now.Add(-time.Minute)
	staleRaw, staleRef = resignProof(t, j, private, stale)
	if _, err := verifier.VerifyPhaseProof(context.Background(), j, staleRef, staleRaw); !errors.Is(err, ErrPhaseProofInvalid) {
		t.Fatalf("expired proof accepted: %v", err)
	}
	competingRaw, competingRef := signedProofBytes(t, j, private, now)
	var competing PhaseProof
	if err := json.Unmarshal(competingRaw, &competing); err != nil {
		t.Fatal(err)
	}
	competing.Claim.Generation++
	competingRaw, competingRef = resignProof(t, j, private, competing)
	if _, err := verifier.VerifyPhaseProof(context.Background(), j, competingRef, competingRaw); err != nil {
		t.Fatalf("individually valid competing proof should verify: %v", err)
	}
	operation := tupleFor(j, verified)
	classification := ClassifyCrashState(j, CatalogTuple{j.Intent.CatalogRevision, j.Intent.CatalogDigest}, operation, EffectObservation{Known: true, Applied: true, Proof: verified})
	if classification.State != CrashStateEffectProven || !classification.Reconcile {
		t.Fatalf("committed classification: %+v", classification)
	}
	operation.Claim.Generation++
	classification = ClassifyCrashState(j, CatalogTuple{j.Intent.CatalogRevision, j.Intent.CatalogDigest}, operation, EffectObservation{Known: true, Applied: true, Proof: verified})
	if classification.State != CrashStateAmbiguous || !classification.Reconcile {
		t.Fatalf("competing claim was not failed closed: %+v", classification)
	}
}

func TestCrashClassifierFailsClosedForLostResponseAndPartialEffect(t *testing.T) {
	j, err := New(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	operation := tupleFor(j, nil)
	for _, observation := range []EffectObservation{{Known: false}, {Known: true, Applied: true}, {Known: true, Applied: false}} {
		got := ClassifyCrashState(j, CatalogTuple{j.Intent.CatalogRevision, j.Intent.CatalogDigest}, operation, observation)
		if observation.Known && !observation.Applied {
			if got.State != CrashStatePending || got.Reconcile {
				t.Fatalf("pending=%+v", got)
			}
			continue
		}
		if got.State != CrashStateAmbiguous || !got.Reconcile {
			t.Fatalf("unsafe crash classification=%+v", got)
		}
	}
}

func signedProofBytes(t *testing.T, j Journal, private ed25519.PrivateKey, observed time.Time) ([]byte, PhaseEvidenceReference) {
	t.Helper()
	intentSHA, err := IntentSHA256(j.Intent)
	if err != nil {
		t.Fatal(err)
	}
	p := PhaseProof{SchemaVersion: PhaseProofSchema, NextPhase: PhaseQuiesce, Accepted: SignedAcceptedIntent{ID: "accept-1", CanonicalBytes: []byte("accepted bytes"), CanonicalSHA256: digestBytes([]byte("accepted bytes")), Signature: ProofSignature{Algorithm: "hmac-sha256", KeyID: "acceptance-1", Value: "accepted"}, OperationID: j.Intent.OperationID, OperationKind: "database.cutover", App: j.Intent.App, CandidateRelease: j.Intent.CandidateRelease, IntentSHA256: intentSHA}, Claim: OperationClaimBinding{OperationID: j.Intent.OperationID, OwnerID: "worker-1", Generation: 1}, AuthorityEpoch: j.Intent.AuthorityGeneration, JournalRevision: j.Revision, JournalReceiptSHA256: j.Receipts[j.Phase], CatalogRevision: j.Intent.CatalogRevision, CatalogDigest: j.Intent.CatalogDigest, Source: binding(j.Intent.Source), Target: binding(j.Intent.Target), WriterInventorySHA256: j.Intent.WriterInventorySHA256, Effects: []ExternalEffect{{ID: "fence-1", Kind: "postgres-role-fence", SHA256: hex.EncodeToString(make([]byte, 32))}}, ObserverID: "observer", ObservedAt: observed, ExpiresAt: observed.Add(time.Hour), Signature: ProofSignature{Algorithm: "ed25519", KeyID: "observer-1"}}
	return resignProof(t, j, private, p)
}
func resignProof(t *testing.T, j Journal, private ed25519.PrivateKey, p PhaseProof) ([]byte, PhaseEvidenceReference) {
	t.Helper()
	canonical, err := CanonicalPhaseProof(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Signature.Value = hex.EncodeToString(ed25519.Sign(private, canonical))
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := NewPhaseEvidenceReference(j, p.NextPhase, digestBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	return raw, ref
}
func binding(t database.TargetIdentity) DatabaseBindingGeneration {
	return DatabaseBindingGeneration{t.ServiceID, t.ServiceGeneration, t.BindingID, t.BindingGeneration}
}
func tupleFor(j Journal, verified *VerifiedPhaseProof) OperationTuple {
	digest, _ := IntentSHA256(j.Intent)
	claim := OperationClaimBinding{OperationID: j.Intent.OperationID, OwnerID: "worker-1", Generation: 1}
	if verified.Valid() {
		claim = verified.Proof().Claim
	}
	return OperationTuple{OperationID: j.Intent.OperationID, App: j.Intent.App, CandidateRelease: j.Intent.CandidateRelease, IntentSHA256: digest, AuthorityEpoch: j.Intent.AuthorityGeneration, Claim: claim, Running: true}
}
