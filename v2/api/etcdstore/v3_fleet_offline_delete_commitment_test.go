package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
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

func TestOfflineDeleteDispatchReservationRejectsIdenticalNoncanonicalRows(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"unknown field":    func(raw []byte) []byte { return append(raw[:len(raw)-1], []byte(`,"extra":true}`)...) },
		"trailing newline": func(raw []byte) []byte { return append(append([]byte(nil), raw...), '\n') },
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _, commitment, raw, admissionAt := admittedOfflineDelete(t)
			if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw, func(context.Context, string, string, string) error { return nil }, func() time.Time { return admissionAt }); err != nil {
				t.Fatal(err)
			}
			head, err := h.s.kv.Get(h.ctx(), h.s.offlineDeleteDispatchReservationKey(commitment.PlanID))
			if err != nil || len(head.Kvs) != 1 {
				t.Fatalf("reservation: %v", err)
			}
			forged := mutate(append([]byte(nil), head.Kvs[0].Value...))
			if _, err := h.s.kv.Txn(h.ctx()).Then(
				clientv3.OpPut(h.s.offlineDeleteDispatchReservationKey(commitment.PlanID), string(forged)),
				clientv3.OpPut(h.s.offlineDeleteDispatchReservationEventKey(commitment.PlanID), string(forged))).Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := h.s.GetOfflineDeleteDispatchReservation(h.ctx(), commitment.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
				t.Fatalf("forged rows accepted: %v", err)
			}
		})
	}
}

func TestOfflineDeleteDispatchReservationReadIsOneRevision(t *testing.T) {
	h, _, _, commitmentA, rawA, admissionAt := admittedOfflineDelete(t)
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), rawA, func(context.Context, string, string, string) error { return nil }, func() time.Time { return admissionAt }); err != nil {
		t.Fatal(err)
	}
	rows, err := h.s.readOfflineDeleteLedger(h.ctx(), commitmentA.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	commitmentB, rawB, err := offline.NewDeleteCommitment(rows.binding, rows.attempt, rows.grant, uuid.NewString(), uuid.NewString(), uuid.NewString(), commitmentA.CommittedAt)
	if err != nil {
		t.Fatal(err)
	}
	dispatchRow, err := h.s.kv.Get(h.ctx(), h.s.offlineDeleteDispatchReservationKey(commitmentA.PlanID))
	if err != nil || len(dispatchRow.Kvs) != 1 {
		t.Fatalf("reservation: %v", err)
	}
	var dispatchA offlineDeleteDispatch
	if json.Unmarshal(dispatchRow.Kvs[0].Value, &dispatchA) != nil {
		t.Fatal("decode reservation")
	}
	dispatchB := dispatchA
	dispatchB.CommitmentSHA256 = offline.DeleteCommitmentSHA256(rawB)
	dispatchB.SupervisorRootID = commitmentB.SupervisorRootID
	dispatchB.SupervisorExecutionID = commitmentB.SupervisorExecutionID
	dispatchB.RuntimeInstanceID = commitmentB.RuntimeInstanceID
	dispatchRawB, _ := json.Marshal(dispatchB)

	write := func(commitment, dispatch []byte) error {
		_, err := h.s.kv.Txn(h.ctx()).Then(
			clientv3.OpPut(h.s.offlineDeleteCommitmentKey(commitmentA.PlanID), string(commitment)),
			clientv3.OpPut(h.s.offlineDeleteCommitmentEventKey(commitmentA.PlanID), string(commitment)),
			clientv3.OpPut(h.s.offlineDeleteDispatchReservationKey(commitmentA.PlanID), string(dispatch)),
			clientv3.OpPut(h.s.offlineDeleteDispatchReservationEventKey(commitmentA.PlanID), string(dispatch))).Commit()
		return err
	}
	errs := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			if err := write(rawB, dispatchRawB); err != nil {
				errs <- err
				return
			}
			if err := write(rawA, dispatchRow.Kvs[0].Value); err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	for i := 0; i < 100; i++ {
		if _, err := h.s.GetOfflineDeleteDispatchReservation(h.ctx(), commitmentA.PlanID); err != nil {
			t.Fatalf("torn reservation read: %v", err)
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
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
	head, err := h.s.kv.Get(h.ctx(), h.s.offlineDeleteDispatchReservationKey(commitment.PlanID))
	if err != nil || len(head.Kvs) != 1 {
		t.Fatalf("dispatch reservation: rows=%d err=%v", len(head.Kvs), err)
	}
	event, err := h.s.kv.Get(h.ctx(), h.s.offlineDeleteDispatchReservationEventKey(commitment.PlanID))
	if err != nil || len(event.Kvs) != 1 || !bytes.Equal(head.Kvs[0].Value, event.Kvs[0].Value) {
		t.Fatalf("dispatch reservation event mismatch: rows=%d err=%v", len(event.Kvs), err)
	}
	recoveredDispatch, err := h.s.GetOfflineDeleteDispatchReservation(h.ctx(), commitment.PlanID)
	if err != nil || !bytes.Equal(recoveredDispatch, head.Kvs[0].Value) {
		t.Fatalf("dispatch reservation recovery differs: err=%v", err)
	}
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), append(append([]byte(nil), raw...), '\n'), verify,
		func() time.Time { return admissionAt }); !errors.Is(err, offline.ErrDeleteCommitment) {
		t.Fatalf("non-canonical commitment admitted: %v", err)
	}
	// Lost-response recovery returns the existing byte-identical reservation;
	// it does not refresh the expired grant or create a second authority row.
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw, verify,
		func() time.Time { return grant.ExpiresAt }); err != nil {
		t.Fatalf("exact reservation recovery: %v", err)
	}
	supervisorErr := errors.New("supervisor runtime is not running")
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw,
		func(context.Context, string, string, string) error { return supervisorErr },
		func() time.Time { return admissionAt }); !errors.Is(err, supervisorErr) {
		t.Fatalf("supervisor refusal lost: %v", err)
	}
}

func TestOfflineDeleteReservationLosesDeterministicPreCASAuthorityRace(t *testing.T) {
	h, binding, _, _, raw, admissionAt := admittedOfflineDelete(t)
	verify := func(context.Context, string, string, string) error {
		_, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetAbandonedKey(binding.PlanID), `{}`)
		return err
	}
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw, verify,
		func() time.Time { return admissionAt }); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
		t.Fatalf("concurrent abandonment admitted: %v", err)
	}
	for _, key := range []string{h.s.offlineDeleteDispatchReservationKey(binding.PlanID), h.s.offlineDeleteDispatchReservationEventKey(binding.PlanID)} {
		response, err := h.s.kv.Get(h.ctx(), key)
		if err != nil || len(response.Kvs) != 0 {
			t.Fatalf("failed CAS wrote %s: rows=%d err=%v", key, len(response.Kvs), err)
		}
	}
}

func TestOfflineDeleteReservationRecoveryRejectsMissingMalformedAndDivergentPairs(t *testing.T) {
	tests := map[string]func(*testing.T, *etcdExecutorHarness, offline.Binding){
		"missing event": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineDeleteDispatchReservationEventKey(b.PlanID)); err != nil {
				t.Fatal(err)
			}
		},
		"malformed pair": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			for _, key := range []string{h.s.offlineDeleteDispatchReservationKey(b.PlanID), h.s.offlineDeleteDispatchReservationEventKey(b.PlanID)} {
				if _, err := h.s.kv.Put(h.ctx(), key, `{}`); err != nil {
					t.Fatal(err)
				}
			}
		},
		"divergent event": func(t *testing.T, h *etcdExecutorHarness, b offline.Binding) {
			if _, err := h.s.kv.Put(h.ctx(), h.s.offlineDeleteDispatchReservationEventKey(b.PlanID), `{}`); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			h, binding, _, _, raw, admissionAt := admittedOfflineDelete(t)
			verify := func(context.Context, string, string, string) error { return nil }
			if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw, verify, func() time.Time { return admissionAt }); err != nil {
				t.Fatal(err)
			}
			mutate(t, h, binding)
			if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw, verify, func() time.Time { return admissionAt }); !errors.Is(err, ErrOfflineDeleteCommitmentConflict) {
				t.Fatalf("corrupt reservation recovered: %v", err)
			}
		})
	}
}

func TestOfflineDeleteReservationReciprocallyBlocksCompetingMutationCAS(t *testing.T) {
	h, binding, _, _, raw, admissionAt := admittedOfflineDelete(t)
	if err := h.s.admitOfflineDeleteWithClock(h.ctx(), raw,
		func(context.Context, string, string, string) error { return nil },
		func() time.Time { return admissionAt }); err != nil {
		t.Fatal(err)
	}
	// Every guarded writer uses this same compare in its final transaction.
	// Exercise representative release, abandonment, GitHub, runner and executor
	// writes against a reservation that won first; none may partially commit.
	for _, lane := range []string{"release", "abandon", "github", "runner", "executor"} {
		t.Run(lane, func(t *testing.T) {
			key := h.s.prefix + "/test/offline-delete-reciprocal-gate/" + lane
			txn, err := h.s.kv.Txn(h.ctx()).If(h.s.noOfflineDeleteDispatchCompare(binding.PlanID)).Then(clientv3.OpPut(key, "bad")).Commit()
			if err != nil || txn.Succeeded {
				t.Fatalf("%s mutation crossed reservation: succeeded=%t err=%v", lane, txn.Succeeded, err)
			}
			response, err := h.s.kv.Get(h.ctx(), key)
			if err != nil || len(response.Kvs) != 0 {
				t.Fatalf("%s mutation partially committed: rows=%d err=%v", lane, len(response.Kvs), err)
			}
		})
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
