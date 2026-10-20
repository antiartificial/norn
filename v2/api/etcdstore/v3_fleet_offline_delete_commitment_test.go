package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/fleet/offline"
)

func admittedOfflineDelete(t *testing.T) (*etcdExecutorHarness, offline.Binding, offline.EffectGrant, offline.DeleteCommitment, []byte, time.Time) {
	t.Helper()
	h, binding, attempt, grant, now := consumedOfflineGrant(t)
	commitAt := now.Add(10 * time.Second)
	commitment, raw, err := h.s.commitOfflineDeleteWithClock(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
		uuid.NewString(), uuid.NewString(), uuid.NewString(), func() time.Time { return commitAt })
	if err != nil {
		t.Fatal(err)
	}
	return h, binding, grant, commitment, raw, commitAt.Add(10 * time.Second)
}

func TestOfflineDeleteCommitmentCASRecoveryAndRestore(t *testing.T) {
	h, b, a, g, _ := consumedOfflineGrant(t)
	root, execution, runtime := uuid.NewString(), uuid.NewString(), uuid.NewString()
	c, raw, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, root, execution, runtime)
	if err != nil {
		t.Fatal(err)
	}
	got, recovered, err := h.s.GetOfflineDeleteCommitment(h.ctx(), b.PlanID)
	if err != nil || got != c || !bytes.Equal(recovered, raw) {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	if _, _, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, root, execution, uuid.NewString()); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
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

func TestOfflineDeleteCommitmentRefreshesGrantExpiryBeforeCAS(t *testing.T) {
	h, b, a, g, _ := consumedOfflineGrant(t)
	reads := 0
	clock := func() time.Time {
		reads++
		if reads == 1 {
			return g.ConsumedAt.Add(time.Nanosecond)
		}
		return g.ExpiresAt
	}
	if _, _, err := h.s.commitOfflineDeleteWithClock(h.ctx(), b.PlanID, a.ID, g.ID,
		uuid.NewString(), uuid.NewString(), uuid.NewString(), clock); !errors.Is(err, offline.ErrDeleteCommitment) {
		t.Fatalf("grant expired during preparation: %v", err)
	}
	if reads != 2 {
		t.Fatalf("clock reads = %d, want fresh pre-CAS read", reads)
	}
	if _, _, err := h.s.GetOfflineDeleteCommitment(h.ctx(), b.PlanID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired grant committed: %v", err)
	}
}

func TestOfflineDeleteCommitmentRejectsChangedAuthority(t *testing.T) {
	h, b, a, g, _ := consumedOfflineGrant(t)
	root, execution, runtime := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetAuthorityEpochKey(), "2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, root, execution, runtime); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
		t.Fatalf("changed epoch accepted: %v", err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetAuthorityEpochKey(), "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineCheckpointEventKey(b.PlanID, 1), "corrupt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, root, execution, runtime); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("changed checkpoint accepted: %v", err)
	}
	if _, _, err := h.s.GetOfflineDeleteCommitment(h.ctx(), b.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("corrupt restore: %v", err)
	}
}

func TestOfflineDeleteCommitmentRestoreRejectsChangedGrant(t *testing.T) {
	h, b, a, g, _ := consumedOfflineGrant(t)
	if _, _, err := h.s.commitOfflineDelete(h.ctx(), b.PlanID, a.ID, g.ID, uuid.NewString(), uuid.NewString(), uuid.NewString()); err != nil {
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

func TestOfflineDeleteAdmissionBindsExactLedgerAndSupervisor(t *testing.T) {
	h, _, grant, commitment, raw, admissionAt := admittedOfflineDelete(t)
	calls := 0
	verify := func(_ context.Context, rootID, executionID, runtimeID string) error {
		calls++
		if rootID != commitment.SupervisorRootID || executionID != commitment.SupervisorExecutionID || runtimeID != commitment.RuntimeInstanceID {
			t.Fatalf("supervisor identities changed: %q %q %q", rootID, executionID, runtimeID)
		}
		return nil
	}
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw, verify, func() time.Time { return admissionAt }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("supervisor verification calls = %d, want 1", calls)
	}
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), append(append([]byte(nil), raw...), '\n'), verify,
		func() time.Time { return admissionAt }); !errors.Is(err, offline.ErrDeleteCommitment) {
		t.Fatalf("non-canonical commitment admitted: %v", err)
	}
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw, verify,
		func() time.Time { return grant.ExpiresAt }); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
		t.Fatalf("expired grant admitted: %v", err)
	}
	supervisorErr := errors.New("supervisor runtime is not running")
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw,
		func(context.Context, string, string, string) error { return supervisorErr },
		func() time.Time { return admissionAt }); !errors.Is(err, supervisorErr) {
		t.Fatalf("supervisor refusal lost: %v", err)
	}
}

func TestOfflineDeleteAdmissionRejectsExpiredCheckpoint(t *testing.T) {
	h, binding, _, _, raw, admissionAt := admittedOfflineDelete(t)
	response, err := h.s.kv.Get(h.ctx(), h.s.offlineCheckpointKey(binding.PlanID))
	if err != nil || len(response.Kvs) != 1 {
		t.Fatalf("checkpoint: %v", err)
	}
	var checkpoint offline.Checkpoint
	if err := json.Unmarshal(response.Kvs[0].Value, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.Prechange.ObservedAt = admissionAt.Add(-5 * time.Minute)
	checkpoint.Prechange.ExpiresAt = admissionAt
	changed, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineCheckpointKey(binding.PlanID), string(changed)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineCheckpointEventKey(binding.PlanID, 1), string(changed)); err != nil {
		t.Fatal(err)
	}
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw,
		func(context.Context, string, string, string) error { return nil },
		func() time.Time { return admissionAt }); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
		t.Fatalf("expired checkpoint admitted: %v", err)
	}
}

func TestOfflineDeleteAdmissionRejectsLostAuthorityAndCompetingLanes(t *testing.T) {
	tests := map[string]func(*testing.T, *etcdExecutorHarness, offline.Binding){
		"owner": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineOwnerConsumptionKey(b.OwnerAuthorizationID)); err != nil {
				t.Fatal(err)
			}
		},
		"fence": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			response, err := h.s.kv.Get(h.ctx(), h.s.fleetTargetFenceKey(b.TargetID))
			if err != nil || len(response.Kvs) != 1 {
				t.Fatalf("fence: %v", err)
			}
			var fence v3FleetTargetFence
			if err := json.Unmarshal(response.Kvs[0].Value, &fence); err != nil {
				t.Fatal(err)
			}
			fence.Held = false
			changed, _ := json.Marshal(fence)
			if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetFenceKey(b.TargetID), string(changed)); err != nil {
				t.Fatal(err)
			}
		},
		"epoch": func(t *testing.T, h *etcdExecutorHarness, _ offline.Binding) {
			if _, err := h.s.kv.Put(h.ctx(), h.s.fleetAuthorityEpochKey(), "2"); err != nil {
				t.Fatal(err)
			}
		},
		"abandoned": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetAbandonedKey(b.PlanID), `{}`); err != nil {
				t.Fatal(err)
			}
		},
		"terminal": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalEventKey(b.PlanID), `{}`); err != nil {
				t.Fatal(err)
			}
		},
		"github lane": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			if _, err := h.s.kv.Put(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(b.PlanID), `{}`); err != nil {
				t.Fatal(err)
			}
		},
		"runner lane": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			if _, err := h.s.kv.Put(h.ctx(), h.s.fleetRunnerAttemptPrefix(b.PlanID)+"1", `{}`); err != nil {
				t.Fatal(err)
			}
		},
		"executor lane": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			if _, err := h.s.kv.Put(h.ctx(), h.s.fleetExecutorMetaKey(b.PlanID, "attempt"), `{}`); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			h, binding, _, _, raw, admissionAt := admittedOfflineDelete(t)
			mutate(t, h, binding)
			if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw,
				func(context.Context, string, string, string) error { return nil },
				func() time.Time { return admissionAt }); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
				t.Fatalf("changed authority admitted: %v", err)
			}
		})
	}
}
