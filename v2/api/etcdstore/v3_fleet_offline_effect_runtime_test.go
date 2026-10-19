package etcdstore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"norn/v2/api/fleet/offline"
)

func TestOfflineEffectAuthorityRequiresIndependentReaders(t *testing.T) {
	if _, err := NewOfflineEffectAuthority(nil, OfflineEffectReaders{}); !errors.Is(err, offline.ErrEffectProofMissing) {
		t.Fatalf("nil store: %v", err)
	}
	if _, err := NewOfflineEffectAuthority(&V3OperationStore{}, OfflineEffectReaders{}); !errors.Is(err, offline.ErrEffectProofMissing) {
		t.Fatalf("missing readers: %v", err)
	}
}

func TestOfflineEffectRecoveryReadsImmutableConsumption(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, offlineTestReobserver{observation: offlineEffectObservation(b, a, now)})
	if err != nil {
		t.Fatal(err)
	}
	authority := &OfflineEffectAuthority{store: h.s}
	recovered, err := authority.RecoverAttempt(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject)
	if err != nil || !reflect.DeepEqual(recovered, grant) {
		t.Fatalf("issued recovery: %+v %v", recovered, err)
	}
	if _, err := authority.RecoverAttempt(h.ctx(), b.PlanID, a.ID, "other-runner"); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong principal recovered grant: %v", err)
	}
	if _, err := authority.Recover(h.ctx(), b.PlanID, a.ID, "wrong", b.RunnerPrincipalSubject); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong grant: %v", err)
	}
	consumed, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject, now, offlineBackendReadFor(b))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err = authority.Recover(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject)
	if err != nil || recovered.Phase != "consumed" || recovered.ConsumedAt == nil || consumed.ConsumedAt == nil || !recovered.ConsumedAt.Equal(*consumed.ConsumedAt) {
		t.Fatalf("event-2 recovery: %+v %v", recovered, err)
	}
}

func TestOfflineEffectManifestAndSecretAreOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOfflineEffectFile(path, 100); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readOfflineEffectFile(path, 100); !errors.Is(err, offline.ErrEffectProofMissing) {
		t.Fatalf("world-readable manifest: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readOfflineEffectFile(link, 100); !errors.Is(err, offline.ErrEffectProofMissing) {
		t.Fatalf("symlinked manifest: %v", err)
	}
	if err := os.WriteFile(path, []byte("part one"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := offlineEffectSecret(path); !errors.Is(err, offline.ErrEffectProofMissing) {
		t.Fatalf("invalid secret: %v", err)
	}
}
