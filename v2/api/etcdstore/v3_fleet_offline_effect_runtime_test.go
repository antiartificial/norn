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
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, offlineEffectIdentity(b, a), now, offlineTestReobserver{observation: offlineEffectObservation(b, a, now)})
	if err != nil {
		t.Fatal(err)
	}
	authority := &OfflineEffectAuthority{store: h.s}
	identity := OfflineRunnerIdentity{Subject: b.RunnerPrincipalSubject, TokenID: a.PrincipalTokenID, DeviceID: a.PrincipalDeviceID}
	recovered, err := authority.RecoverAttempt(h.ctx(), b.PlanID, a.ID, identity)
	if err != nil || !reflect.DeepEqual(recovered, grant) {
		t.Fatalf("issued recovery: %+v %v", recovered, err)
	}
	other := identity
	other.Subject = "other-runner"
	if _, err := authority.RecoverAttempt(h.ctx(), b.PlanID, a.ID, other); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong principal recovered grant: %v", err)
	}
	other = identity
	other.TokenID = "token-2"
	if _, err := authority.RecoverAttempt(h.ctx(), b.PlanID, a.ID, other); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong token recovered grant: %v", err)
	}
	other = identity
	other.DeviceID = "device-2"
	if _, err := authority.RecoverAttempt(h.ctx(), b.PlanID, a.ID, other); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong device recovered grant: %v", err)
	}
	if _, err := authority.Recover(h.ctx(), b.PlanID, a.ID, "wrong", identity); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong grant: %v", err)
	}
	consumed, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, offlineEffectIdentity(b, a), now, offlineBackendReadFor(b))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err = authority.Recover(h.ctx(), b.PlanID, a.ID, grant.ID, identity)
	if err != nil || recovered.Phase != "consumed" || recovered.ConsumedAt == nil || consumed.ConsumedAt == nil || !recovered.ConsumedAt.Equal(*consumed.ConsumedAt) {
		t.Fatalf("event-2 recovery: %+v %v", recovered, err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineEffectGrantEventKey(b.PlanID, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Recover(h.ctx(), b.PlanID, a.ID, grant.ID, identity); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("partial event-2 restore recovered: %v", err)
	}
}

func TestOfflineEffectSameSubjectDifferentCredentialCannotIssueOrConsume(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	identity := OfflineRunnerIdentity{Subject: b.RunnerPrincipalSubject, TokenID: a.PrincipalTokenID, DeviceID: a.PrincipalDeviceID}
	authority := &OfflineEffectAuthority{store: h.s, observer: offlineTestReobserver{observation: offlineEffectObservation(b, a, now)}, backend: offlineBackendReadFor(b)}
	other := identity
	other.TokenID = "new-token"
	if _, err := authority.Issue(h.ctx(), b.PlanID, a.ID, other, now); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("same subject new token issued: %v", err)
	}
	other = identity
	other.DeviceID = "new-device"
	if _, err := authority.Issue(h.ctx(), b.PlanID, a.ID, other, now); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("same subject new device issued: %v", err)
	}
	grant, err := authority.Issue(h.ctx(), b.PlanID, a.ID, identity, now)
	if err != nil {
		t.Fatal(err)
	}
	other = identity
	other.TokenID = "new-token"
	if _, err := authority.Consume(h.ctx(), b.PlanID, a.ID, grant.ID, other, now); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("same subject new token consumed: %v", err)
	}
	other = identity
	other.DeviceID = "new-device"
	if _, err := authority.Consume(h.ctx(), b.PlanID, a.ID, grant.ID, other, now); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("same subject new device consumed: %v", err)
	}
	if _, err := authority.Consume(h.ctx(), b.PlanID, a.ID, grant.ID, identity, now); err != nil {
		t.Fatalf("bound credential refused: %v", err)
	}
}

func TestOfflineEffectRecoveryConcurrentConsumptionSeesWholeEventSnapshot(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	identity := offlineEffectIdentity(b, a)
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, identity, now, offlineTestReobserver{observation: offlineEffectObservation(b, a, now)})
	if err != nil {
		t.Fatal(err)
	}
	authority := NewOfflineEffectRecovery(h.s)
	result := make(chan error, 1)
	go func() {
		_, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, identity, now, offlineBackendReadFor(b))
		result <- err
	}()
	for i := 0; i < 20; i++ {
		observed, err := authority.Recover(h.ctx(), b.PlanID, a.ID, grant.ID, identity)
		if err != nil {
			t.Fatalf("concurrent snapshot: %v", err)
		}
		if observed.Phase == "issued" && observed.ConsumedAt != nil {
			t.Fatal("issued cursor had event-2 timestamp")
		}
		if observed.Phase == "consumed" && observed.ConsumedAt == nil {
			t.Fatal("consumed cursor missing event-2 timestamp")
		}
		if observed.Phase != "issued" && observed.Phase != "consumed" {
			t.Fatalf("unknown phase %q", observed.Phase)
		}
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if observed, err := authority.Recover(h.ctx(), b.PlanID, a.ID, grant.ID, identity); err != nil || observed.Phase != "consumed" {
		t.Fatalf("final event-2 snapshot: %+v %v", observed, err)
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
