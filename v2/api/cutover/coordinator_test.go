package cutover

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"norn/v2/api/archive"
)

func TestActivationCoordinatorReconcilesCrashAfterGenerationEffect(t *testing.T) {
	ctx := context.Background()
	j := finalSyncJournal(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := signedActivationProof(t, j, private, now, 42)
	archiveStore := &memoryArchive{objects: map[string][]byte{}}
	ref, err := RetainPhaseEvidence(ctx, archiveStore, j, PhaseActivate, raw)
	if err != nil {
		t.Fatal(err)
	}
	journal := &memoryActivationJournal{journal: j}
	effect := &memoryActivationEffect{failAfterApply: true}
	coordinator := ActivationCoordinator{Journal: journal, Archive: archiveStore, Verifier: activationVerifier(pub, now), Effect: effect}

	// The effect commits remotely but the process loses its response before the
	// journal CAS. The original journal remains FinalSync and a retry must use
	// readback rather than applying a second consumer generation.
	if _, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref); !errors.Is(err, ErrActivationBlocked) {
		t.Fatalf("lost response did not block: %v", err)
	}
	if journal.journal.Phase != PhaseFinalSync || !effect.installed || effect.applyCalls != 1 {
		t.Fatalf("crash boundary journal=%s installed=%v applies=%d", journal.journal.Phase, effect.installed, effect.applyCalls)
	}
	effect.failAfterApply = false
	// The phase permit can expire while the coordinator is down. It may still
	// reconcile the installed exact generation into the journal, but cannot
	// authorize a fresh apply.
	coordinator.Verifier = activationVerifier(pub, now.Add(2*time.Hour))
	activated, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref)
	if err != nil {
		t.Fatalf("restart reconciliation: %v", err)
	}
	if activated.Phase != PhaseActivate || activated.EvidenceReferences[PhaseActivate] != ref || effect.applyCalls != 1 || effect.observeCalls < 2 {
		t.Fatalf("restart result phase=%s ref=%+v applies=%d observes=%d", activated.Phase, activated.EvidenceReferences[PhaseActivate], effect.applyCalls, effect.observeCalls)
	}
	// A second restart verifies the committed archive proof and exact live
	// generation; a wrong/unknown generation never becomes a completed result.
	if _, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref); err != nil || effect.applyCalls != 1 {
		t.Fatalf("committed restart: err=%v applies=%d", err, effect.applyCalls)
	}
	// Expiry closes the permission to apply a new effect, but does not erase a
	// committed boundary. Recovery still requires the exact live observation.
	if _, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref); err != nil || effect.applyCalls != 1 {
		t.Fatalf("expired committed proof did not recover historically: err=%v applies=%d", err, effect.applyCalls)
	}
	effect.conflict = true
	if _, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref); !errors.Is(err, ErrActivationBlocked) {
		t.Fatalf("generation drift was accepted: %v", err)
	}
}

func TestActivationCoordinatorExpiredUncommittedPermitNeverApplies(t *testing.T) {
	ctx := context.Background()
	j := finalSyncJournal(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := signedActivationProof(t, j, private, now, 42)
	archiveStore := &memoryArchive{objects: map[string][]byte{}}
	ref, err := RetainPhaseEvidence(ctx, archiveStore, j, PhaseActivate, raw)
	if err != nil {
		t.Fatal(err)
	}
	effect := &memoryActivationEffect{}
	coordinator := ActivationCoordinator{Journal: &memoryActivationJournal{journal: j}, Archive: archiveStore, Verifier: activationVerifier(pub, now.Add(2*time.Hour)), Effect: effect}
	if _, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref); !errors.Is(err, ErrActivationBlocked) || effect.applyCalls != 0 {
		t.Fatalf("expired uncommitted proof applied generation: err=%v calls=%d", err, effect.applyCalls)
	}
	effect.conflict = true
	if _, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref); !errors.Is(err, ErrActivationBlocked) || effect.applyCalls != 0 {
		t.Fatalf("expired conflicting generation applied: err=%v calls=%d", err, effect.applyCalls)
	}
}

func TestActivationCoordinatorReconcilesLostJournalCASResponse(t *testing.T) {
	ctx := context.Background()
	j := finalSyncJournal(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := signedActivationProof(t, j, private, now, 42)
	archiveStore := &memoryArchive{objects: map[string][]byte{}}
	ref, err := RetainPhaseEvidence(ctx, archiveStore, j, PhaseActivate, raw)
	if err != nil {
		t.Fatal(err)
	}
	journal := &memoryActivationJournal{journal: j, failAfterAdvance: true}
	effect := &memoryActivationEffect{}
	coordinator := ActivationCoordinator{Journal: journal, Archive: archiveStore, Verifier: activationVerifier(pub, now), Effect: effect}
	if _, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref); !errors.Is(err, ErrActivationBlocked) {
		t.Fatalf("lost journal response did not block: %v", err)
	}
	if journal.journal.Phase != PhaseActivate || effect.applyCalls != 1 {
		t.Fatalf("lost CAS did not persist exact activation boundary: phase=%s applies=%d", journal.journal.Phase, effect.applyCalls)
	}
	journal.failAfterAdvance = false
	if _, err := coordinator.ResumeActivation(ctx, j.Intent.OperationID, ref); err != nil || effect.applyCalls != 1 {
		t.Fatalf("lost CAS restart replayed effect: err=%v applies=%d", err, effect.applyCalls)
	}
}

func TestJournalBeforeActivationDoesNotMutateCommittedJournal(t *testing.T) {
	j := finalSyncJournal(t)
	ref, err := NewPhaseEvidenceReference(j, PhaseActivate, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	j, err = j.AdvanceWithEvidence(ref)
	if err != nil {
		t.Fatal(err)
	}
	before := journalBeforeActivation(j)
	if j.Phase != PhaseActivate || j.Revision != 4 || j.EvidenceReferences[PhaseActivate] != ref || j.Receipts[PhaseActivate] == "" {
		t.Fatalf("committed journal was mutated: %+v", j)
	}
	before.Receipts[PhaseFinalSync] = "changed"
	if j.Receipts[PhaseFinalSync] == "changed" {
		t.Fatal("reconstructed journal shares receipt map with committed journal")
	}
}

func TestVerifiedActivationCommitRejectsForgeryAndBindingDrift(t *testing.T) {
	j := finalSyncJournal(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, ref := signedActivationProof(t, j, private, now, 42)
	proof, err := activationVerifier(pub, now).VerifyPhaseProof(context.Background(), j, ref, raw)
	if err != nil {
		t.Fatal(err)
	}
	request, err := activationRequest(j, proof)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := newVerifiedActivationCommit(j, ref, proof, request)
	if err != nil || !commit.ValidFor(j) {
		t.Fatalf("verified commit: commit=%v err=%v", commit, err)
	}
	if _, ok := (&VerifiedActivationCommit{}).EvidenceReference(); ok || (&VerifiedActivationCommit{}).ValidFor(j) {
		t.Fatal("unminted activation commit was accepted")
	}
	drifted := j
	drifted.Revision++
	if commit.ValidFor(drifted) {
		t.Fatal("commit accepted a different journal revision")
	}
	otherRef := ref
	otherRef.EvidenceSHA256 = strings.Repeat("e", 64)
	if got, ok := commit.EvidenceReference(); !ok || got != ref || got == otherRef {
		t.Fatalf("commit reference binding got=%+v ok=%v", got, ok)
	}
	wrongRequest := request
	wrongRequest.Consumer.Generation++
	if _, err := newVerifiedActivationCommit(j, ref, proof, wrongRequest); !errors.Is(err, ErrActivationBlocked) {
		t.Fatalf("mismatched consumer request minted commit: %v", err)
	}
}

func TestActivationCoordinatorFailsClosedWithoutRetainedVerifiedPermit(t *testing.T) {
	j := finalSyncJournal(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := signedActivationProof(t, j, private, now, 42)
	archiveStore := &memoryArchive{objects: map[string][]byte{}}
	ref, err := RetainPhaseEvidence(context.Background(), archiveStore, j, PhaseActivate, raw)
	if err != nil {
		t.Fatal(err)
	}
	// A signed but unretained replacement is not accepted, and neither is an
	// otherwise valid permit missing the source fence/final-sync assertions.
	key, err := PhaseEvidenceKey(j, PhaseActivate)
	if err != nil {
		t.Fatal(err)
	}
	delete(archiveStore.objects, key)
	effect := &memoryActivationEffect{}
	coordinator := ActivationCoordinator{Journal: &memoryActivationJournal{journal: j}, Archive: archiveStore, Verifier: activationVerifier(pub, now), Effect: effect}
	if _, err := coordinator.ResumeActivation(context.Background(), j.Intent.OperationID, ref); !errors.Is(err, ErrActivationBlocked) || effect.applyCalls != 0 {
		t.Fatalf("missing retained permit activated: err=%v calls=%d", err, effect.applyCalls)
	}
	// Re-retain bytes signed with a nonconforming effect inventory under the
	// current journal edge. Signature validity alone cannot activate it.
	badRaw, _ := signedActivationProof(t, j, private, now, 42)
	var bad PhaseProof
	if err := json.Unmarshal(badRaw, &bad); err != nil {
		t.Fatal(err)
	}
	bad.Effects = []ExternalEffect{{ID: "other", Kind: "other", SHA256: strings.Repeat("a", 64)}}
	badRaw, badRef := resignProof(t, j, private, bad)
	if _, err := RetainPhaseEvidence(context.Background(), archiveStore, j, PhaseActivate, badRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ResumeActivation(context.Background(), j.Intent.OperationID, badRef); !errors.Is(err, ErrActivationBlocked) || effect.applyCalls != 0 {
		t.Fatalf("incomplete assertions activated: err=%v calls=%d", err, effect.applyCalls)
	}
}

func finalSyncJournal(t *testing.T) Journal {
	t.Helper()
	j, err := New(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	for index, phase := range []Phase{PhaseQuiesce, PhaseFinalSync} {
		digest := strings.Repeat("a", 64)
		if index == 1 {
			digest = strings.Repeat("b", 64)
		}
		ref, err := NewPhaseEvidenceReference(j, phase, digest)
		if err != nil {
			t.Fatal(err)
		}
		j, err = j.AdvanceWithEvidence(ref)
		if err != nil {
			t.Fatal(err)
		}
	}
	return j
}

func signedActivationProof(t *testing.T, j Journal, private ed25519.PrivateKey, now time.Time, generation uint64) ([]byte, PhaseEvidenceReference) {
	t.Helper()
	intentSHA, err := IntentSHA256(j.Intent)
	if err != nil {
		t.Fatal(err)
	}
	p := PhaseProof{SchemaVersion: PhaseProofSchema, NextPhase: PhaseActivate,
		Accepted: SignedAcceptedIntent{ID: "accept-1", CanonicalBytes: []byte("accepted bytes"), CanonicalSHA256: digestBytes([]byte("accepted bytes")), Signature: ProofSignature{Algorithm: "hmac-sha256", KeyID: "acceptance-1", Value: "accepted"}, OperationID: j.Intent.OperationID, OperationKind: "database.cutover", App: j.Intent.App, CandidateRelease: j.Intent.CandidateRelease, IntentSHA256: intentSHA},
		Claim:    OperationClaimBinding{OperationID: j.Intent.OperationID, OwnerID: "worker-1", Generation: 9}, AuthorityEpoch: j.Intent.AuthorityGeneration, JournalRevision: j.Revision, JournalReceiptSHA256: j.Receipts[j.Phase], CatalogRevision: j.Intent.CatalogRevision, CatalogDigest: j.Intent.CatalogDigest, Source: binding(j.Intent.Source), Target: binding(j.Intent.Target), WriterInventorySHA256: j.Intent.WriterInventorySHA256,
		Consumer: ConsumerGeneration{Generation: generation, Target: binding(j.Intent.Target), CredentialDigest: strings.Repeat("d", 64)},
		Effects:  []ExternalEffect{{ID: "fence-1", Kind: "source-writer-fence", SHA256: strings.Repeat("a", 64)}, {ID: "sync-1", Kind: "final-sync-checkpoint", SHA256: strings.Repeat("b", 64)}}, ObserverID: "observer", ObservedAt: now, ExpiresAt: now.Add(time.Hour), Signature: ProofSignature{Algorithm: "ed25519", KeyID: "observer-1"}}
	return resignProof(t, j, private, p)
}

func activationVerifier(pub ed25519.PublicKey, now time.Time) PhaseProofVerifier {
	return PhaseProofVerifier{TrustedObserverKeyID: "observer-1", TrustedObserverKey: pub, Now: func() time.Time { return now }, Acceptance: acceptanceProofFunc(func(_ context.Context, a SignedAcceptedIntent) error {
		if a.Signature.Value != "accepted" {
			return errors.New("untrusted acceptance")
		}
		return nil
	}), Readback: readbackFunc(func(_ context.Context, e ExternalEffect) error {
		if e.ID != "fence-1" && e.ID != "sync-1" {
			return errors.New("unknown effect")
		}
		return nil
	})}
}

type memoryActivationJournal struct {
	journal          Journal
	failAfterAdvance bool
}

func (m *memoryActivationJournal) LoadActivationJournal(_ context.Context, id string) (Journal, error) {
	if id != m.journal.Intent.OperationID {
		return Journal{}, errors.New("not found")
	}
	return m.journal, nil
}
func (m *memoryActivationJournal) AdvanceActivationJournal(_ context.Context, current Journal, commit *VerifiedActivationCommit) (Journal, error) {
	if current.Revision != m.journal.Revision || current.Phase != m.journal.Phase {
		return Journal{}, errors.New("cas lost")
	}
	if !commit.ValidFor(current) {
		return Journal{}, errors.New("unverified activation commit")
	}
	ref, ok := commit.EvidenceReference()
	if !ok {
		return Journal{}, errors.New("missing activation evidence")
	}
	next, err := current.AdvanceWithEvidence(ref)
	if err != nil {
		return Journal{}, err
	}
	m.journal = next
	if m.failAfterAdvance {
		return Journal{}, errors.New("lost CAS response")
	}
	return next, nil
}

type memoryActivationEffect struct {
	installed, conflict, failAfterApply bool
	applyCalls, observeCalls            int
}

func (m *memoryActivationEffect) ObserveActivation(_ context.Context, req ActivationRequest) (ActivationObservation, error) {
	m.observeCalls++
	if req.Consumer.Generation != 42 || req.Claim.Generation != 9 || req.AuthorityEpoch != 7 || req.SourceFence.ID != "fence-1" || req.FinalSync.ID != "sync-1" {
		return ActivationConflict, nil
	}
	if m.conflict {
		return ActivationConflict, nil
	}
	if m.installed {
		return ActivationInstalled, nil
	}
	return ActivationAbsent, nil
}
func (m *memoryActivationEffect) ApplyActivation(_ context.Context, _ ActivationRequest) error {
	m.applyCalls++
	m.installed = true
	if m.failAfterApply {
		return errors.New("lost response")
	}
	return nil
}

type memoryArchive struct{ objects map[string][]byte }

func (m *memoryArchive) PutImmutable(_ context.Context, key string, data []byte) (archive.ObjectInfo, error) {
	if prior, ok := m.objects[key]; ok && string(prior) != string(data) {
		return archive.ObjectInfo{}, archive.ErrImmutableConflict
	}
	m.objects[key] = append([]byte(nil), data...)
	return archive.ObjectInfo{Key: key, SHA256: digestBytes(data), Size: int64(len(data))}, nil
}
func (m *memoryArchive) Get(_ context.Context, key string, max int64) ([]byte, archive.ObjectInfo, error) {
	b, ok := m.objects[key]
	if !ok {
		return nil, archive.ObjectInfo{}, archive.ErrObjectNotFound
	}
	if int64(len(b)) > max {
		return nil, archive.ObjectInfo{}, archive.ErrObjectTooLarge
	}
	return append([]byte(nil), b...), archive.ObjectInfo{Key: key, SHA256: digestBytes(b), Size: int64(len(b))}, nil
}
func (m *memoryArchive) Verify(_ context.Context, info archive.ObjectInfo) error {
	b, ok := m.objects[info.Key]
	if !ok || info.SHA256 != digestBytes(b) || info.Size != int64(len(b)) {
		return archive.ErrObjectCorrupt
	}
	return nil
}
func (m *memoryArchive) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

var _ archive.Store = (*memoryArchive)(nil)
