package etcdstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"norn/v2/api/fleet/offline"
)

func TestOfflineDeleteCommitmentCASRecoveryAndRestore(t *testing.T) {
	h, b, a, g, now := consumedOfflineGrant(t)
	root, execution, runtime := uuid.NewString(), uuid.NewString(), uuid.NewString()
	c, raw, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, root, execution, runtime, now)
	if err != nil {
		t.Fatal(err)
	}
	got, recovered, err := h.s.GetOfflineDeleteCommitment(h.ctx(), b.PlanID)
	if err != nil || got != c || !bytes.Equal(recovered, raw) {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	if _, _, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, root, execution, uuid.NewString(), now); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
		t.Fatalf("second runtime admitted: %v", err)
	}
	fence, _, err := h.s.GetFleetTargetFence(h.ctx(), b.TargetID)
	if err != nil || !fence.Held {
		t.Fatalf("commitment released fence: %+v %v", fence, err)
	}
	// Missing or divergent archive-restored halves must fail closed.
	for _, key := range []string{h.s.offlineDeleteCommitmentKey(b.PlanID), h.s.offlineDeleteCommitmentEventKey(b.PlanID)} {
		if _, err := h.s.kv.Delete(h.ctx(), key); err != nil {
			t.Fatal(err)
		}
		if _, _, err := h.s.GetOfflineDeleteCommitment(h.ctx(), b.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
			t.Fatalf("missing half %s: %v", key, err)
		}
		if _, err := h.s.kv.Put(h.ctx(), key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineDeleteCommitmentEventKey(b.PlanID), "different"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.GetOfflineDeleteCommitment(h.ctx(), b.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("diverged event: %v", err)
	}
}

func TestOfflineDeleteCommitmentRejectsChangedAuthority(t *testing.T) {
	h, b, a, g, now := consumedOfflineGrant(t)
	root, execution, runtime := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetAuthorityEpochKey(), "2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, root, execution, runtime, now); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
		t.Fatalf("changed epoch accepted: %v", err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetAuthorityEpochKey(), "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineCheckpointEventKey(b.PlanID, 1), "corrupt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, root, execution, runtime, now); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("changed checkpoint accepted: %v", err)
	}
	if _, _, err := h.s.GetOfflineDeleteCommitment(h.ctx(), b.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("corrupt restore: %v", err)
	}
}

func TestOfflineDeleteCommitmentRestoreRejectsChangedGrant(t *testing.T) {
	h, b, a, g, now := consumedOfflineGrant(t)
	if _, _, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, uuid.NewString(), uuid.NewString(), uuid.NewString(), now); err != nil {
		t.Fatal(err)
	}
	changed := g
	changed.IndependentlyObservedSHA256 = offline.DeleteCommitmentSHA256([]byte("other"))
	raw, _ := json.Marshal(changed)
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineEffectGrantKey(b.PlanID), string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineEffectGrantEventKey(b.PlanID, 2), string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.GetOfflineDeleteCommitment(h.ctx(), b.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("changed grant restored: %v", err)
	}
}
