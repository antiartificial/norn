package cutover

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/archive"
)

type authorityEvidenceCheck struct{}

func (authorityEvidenceCheck) VerifyAuthorityEvidence(_ context.Context, _ AuthorityManifest, e AuthorityEvidence, raw []byte) error {
	if string(raw) != "evidence:"+e.ID {
		return errors.New("unexpected evidence bytes")
	}
	return nil
}

// TestAuthorityLogVerifier exercises durable reopen, the complete signed
// chain and the distinction between historical recovery and a live permit.
func TestAuthorityLogVerifierDurableReopenAndExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "authority")
	store, err := archive.OpenLocal(root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	pub, private, _ := ed25519.GenerateKey(rand.Reader)
	buildAuthorityChain(t, ctx, store, "cutover-1", private, now, []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := archive.OpenLocalReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	v := AuthorityLogVerifier{Archive: reader, TrustRoots: []AuthorityTrustKey{{KeyID: "key-1", PublicKey: pub}}, EvidenceVerifier: authorityEvidenceCheck{}, Now: func() time.Time { return now }}
	chain, err := v.VerifyCurrent(ctx, "cutover-1")
	if err != nil || !chain.AuthorizesNewEffect() {
		t.Fatalf("current chain = %#v, %v", chain, err)
	}
	if _, digest, ok := chain.LastManifest(); !ok || digest == "" {
		t.Fatal("last durable boundary missing")
	}
	v.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := v.VerifyCurrent(ctx, "cutover-1"); !errors.Is(err, ErrAuthorityLogInvalid) {
		t.Fatalf("expired current chain err=%v", err)
	}
	historical, err := v.VerifyHistorical(ctx, "cutover-1")
	if err != nil || historical.AuthorizesNewEffect() {
		t.Fatalf("historical chain = %#v, %v", historical, err)
	}
}

func TestAuthorityLogVerifierRecoversEveryNonEmptyCheckpoint(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		phases []Phase
		valid  bool
	}{
		{name: "empty archive", valid: false},
		{name: "prepare", phases: []Phase{PhasePrepare}, valid: true},
		{name: "quiesce", phases: []Phase{PhasePrepare, PhaseQuiesce}, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := archive.OpenLocal(filepath.Join(t.TempDir(), "authority"), 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			pub, private, _ := ed25519.GenerateKey(rand.Reader)
			if tc.valid {
				buildAuthorityChain(t, ctx, store, "cutover-1", private, now, tc.phases)
			}
			chain, err := authorityVerifier(store, pub, now).VerifyHistorical(ctx, "cutover-1")
			if tc.valid && (err != nil || !chain.Valid()) {
				t.Fatalf("chain=%#v err=%v", chain, err)
			}
			if !tc.valid && !errors.Is(err, ErrAuthorityLogInvalid) {
				t.Fatalf("empty archive err=%v", err)
			}
		})
	}
}

func TestAuthorityLogVerifierRejectsChainAndEvidenceFailures(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mutate func([]AuthorityManifest)
	}{
		{"bad signature", func(ms []AuthorityManifest) { ms[1].Signature.KeyID = "key-2" }},
		{"bad prior hash", func(ms []AuthorityManifest) { ms[1].PriorManifestSHA256 = strings.Repeat("a", 64) }},
		{"tuple drift", func(ms []AuthorityManifest) { ms[1].Tuple.Target.BindingGeneration++ }},
		{"replayed evidence", func(ms []AuthorityManifest) { ms[1].Evidence = ms[0].Evidence }},
		{"phase skip", func(ms []AuthorityManifest) { ms[1].Phase = PhaseFinalSync }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := archive.OpenLocal(filepath.Join(t.TempDir(), "authority"), 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			pub, private, _ := ed25519.GenerateKey(rand.Reader)
			manifests := buildAuthorityManifests(t, ctx, store, "cutover-1", private, now, []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync})
			tc.mutate(manifests)
			publishAuthorityManifests(t, ctx, store, manifests, private)
			v := authorityVerifier(store, pub, now)
			if _, err := v.VerifyCurrent(ctx, "cutover-1"); !errors.Is(err, ErrAuthorityLogInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAuthorityLogVerifierRejectsGapsForksMissingEvidenceAndRevokedKeys(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		publish func(t *testing.T, ctx context.Context, s archive.Store, private ed25519.PrivateKey, now time.Time)
		verify  func(AuthorityLogVerifier) AuthorityLogVerifier
	}{
		{"gap", func(t *testing.T, ctx context.Context, s archive.Store, p ed25519.PrivateKey, n time.Time) {
			ms := buildAuthorityManifests(t, ctx, s, "cutover-1", p, n, []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync})
			ms[1].Sequence = 3
			publishAuthorityManifests(t, ctx, s, []AuthorityManifest{ms[0], ms[1], ms[2]}, p)
		}, func(v AuthorityLogVerifier) AuthorityLogVerifier { return v }},
		{"fork", func(t *testing.T, ctx context.Context, s archive.Store, p ed25519.PrivateKey, n time.Time) {
			ms := buildAuthorityManifests(t, ctx, s, "cutover-1", p, n, []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync})
			publishAuthorityManifests(t, ctx, s, ms, p)
			fork := ms[1]
			fork.RecoveryPolicy = "different"
			publishAuthorityManifests(t, ctx, s, []AuthorityManifest{fork}, p)
		}, func(v AuthorityLogVerifier) AuthorityLogVerifier { return v }},
		{"missing evidence", func(t *testing.T, ctx context.Context, s archive.Store, p ed25519.PrivateKey, n time.Time) {
			ms := buildAuthorityManifests(t, ctx, s, "cutover-1", p, n, []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync})
			ms[2].Evidence[0].Key = "authority-evidence/missing"
			ms[2].Evidence[0].SHA256 = strings.Repeat("b", 64)
			publishAuthorityManifests(t, ctx, s, ms, p)
		}, func(v AuthorityLogVerifier) AuthorityLogVerifier { return v }},
		{"revoked key", func(t *testing.T, ctx context.Context, s archive.Store, p ed25519.PrivateKey, n time.Time) {
			buildAuthorityChain(t, ctx, s, "cutover-1", p, n, []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync})
		}, func(v AuthorityLogVerifier) AuthorityLogVerifier {
			at := now
			v.TrustRoots[0].RevokedAt = &at
			return v
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := archive.OpenLocal(filepath.Join(t.TempDir(), "authority"), 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			pub, private, _ := ed25519.GenerateKey(rand.Reader)
			tc.publish(t, ctx, s, private, now)
			v := tc.verify(authorityVerifier(s, pub, now))
			if _, err := v.VerifyHistorical(ctx, "cutover-1"); !errors.Is(err, ErrAuthorityLogInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAuthorityLogVerifierAcceptsRotatedKeys(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s, err := archive.OpenLocal(filepath.Join(t.TempDir(), "authority"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pub1, private1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, private2, _ := ed25519.GenerateKey(rand.Reader)
	ms := buildAuthorityManifests(t, ctx, s, "cutover-1", private1, now, []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync})
	ms[1].Signature.KeyID, ms[2].Signature.KeyID = "key-2", "key-2"
	raw0, _ := signedAuthorityManifest(ms[0], private1)
	sum0 := sha256.Sum256(raw0)
	ms[1].PriorManifestSHA256 = hex.EncodeToString(sum0[:])
	raw1, _ := signedAuthorityManifest(ms[1], private2)
	sum1 := sha256.Sum256(raw1)
	ms[2].PriorManifestSHA256 = hex.EncodeToString(sum1[:])
	publishAuthorityManifests(t, ctx, s, ms[:1], private1)
	publishAuthorityManifests(t, ctx, s, ms[1:], private2)
	v := AuthorityLogVerifier{Archive: s, TrustRoots: []AuthorityTrustKey{{KeyID: "key-1", PublicKey: pub1, ValidUntil: now.Add(time.Hour)}, {KeyID: "key-2", PublicKey: pub2, ValidFrom: now}}, EvidenceVerifier: authorityEvidenceCheck{}, Now: func() time.Time { return now.Add(30 * time.Minute) }}
	if _, err := v.VerifyCurrent(ctx, "cutover-1"); err != nil {
		t.Fatalf("rotated chain: %v", err)
	}
}

func authorityVerifier(s archive.Reader, pub ed25519.PublicKey, now time.Time) AuthorityLogVerifier {
	return AuthorityLogVerifier{Archive: s, TrustRoots: []AuthorityTrustKey{{KeyID: "key-1", PublicKey: pub}}, EvidenceVerifier: authorityEvidenceCheck{}, Now: func() time.Time { return now }}
}
func buildAuthorityChain(t *testing.T, ctx context.Context, s archive.Store, id string, private ed25519.PrivateKey, now time.Time, phases []Phase) {
	publishAuthorityManifests(t, ctx, s, buildAuthorityManifests(t, ctx, s, id, private, now, phases), private)
}
func buildAuthorityManifests(t *testing.T, ctx context.Context, s archive.Store, id string, private ed25519.PrivateKey, now time.Time, phases []Phase) []AuthorityManifest {
	t.Helper()
	out := make([]AuthorityManifest, 0, len(phases))
	prior := ""
	for i, phase := range phases {
		e := AuthorityEvidence{ID: fmt.Sprintf("e-%d", i+1), Kind: "readback", Key: fmt.Sprintf("authority-evidence/%s/%d", id, i+1)}
		raw := []byte("evidence:" + e.ID)
		sum := sha256.Sum256(raw)
		e.SHA256 = hex.EncodeToString(sum[:])
		if _, err := s.PutImmutable(ctx, e.Key, raw); err != nil {
			t.Fatal(err)
		}
		m := AuthorityManifest{SchemaVersion: AuthorityManifestSchema, LogID: id, Sequence: uint64(i + 1), Phase: phase, Tuple: testIntent(), PriorManifestSHA256: prior, Evidence: []AuthorityEvidence{e}, RecoveryPolicy: "repair-forward", SignedAt: now, ExpiresAt: now.Add(time.Hour), Signature: AuthoritySignature{Algorithm: "ed25519", KeyID: "key-1"}}
		raw, err := signedAuthorityManifest(m, private)
		if err != nil {
			t.Fatal(err)
		}
		sum = sha256.Sum256(raw)
		prior = hex.EncodeToString(sum[:])
		out = append(out, m)
	}
	return out
}
func publishAuthorityManifests(t *testing.T, ctx context.Context, s archive.Store, manifests []AuthorityManifest, private ed25519.PrivateKey) {
	t.Helper()
	for _, m := range manifests {
		raw, err := signedAuthorityManifest(m, private)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		key, err := AuthorityManifestKey(m.LogID, m.Sequence, hex.EncodeToString(sum[:]))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.PutImmutable(ctx, key, raw); err != nil {
			t.Fatal(err)
		}
	}
}
func signedAuthorityManifest(m AuthorityManifest, private ed25519.PrivateKey) ([]byte, error) {
	canonical, err := CanonicalAuthorityManifest(m)
	if err != nil {
		return nil, err
	}
	m.Signature.Value = hex.EncodeToString(ed25519.Sign(private, canonical))
	return json.Marshal(m)
}
