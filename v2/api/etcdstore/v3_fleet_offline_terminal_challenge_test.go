package etcdstore

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet/offline"
)

func installTerminalObserverTrust(t *testing.T, h *etcdExecutorHarness) {
	t.Helper()
	trust := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 1,
		ObserverSubject: "management-observer", KeyID: "observer-key-1",
		PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}
	raw, err := json.Marshal(trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), string(raw)); err != nil {
		t.Fatal(err)
	}
}

func issueTerminalChallenge(t *testing.T) (*etcdExecutorHarness, offline.Binding, offline.Attempt, offline.EffectGrant, offline.TerminalChallengeV1) {
	t.Helper()
	h, binding, attempt, grant, _ := consumedOfflineGrant(t)
	installTerminalObserverTrust(t, h)
	challenge, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
		"fleet-verifier", "management-observer")
	if err != nil {
		t.Fatal(err)
	}
	return h, binding, attempt, grant, challenge
}

func TestOfflineTerminalChallengeIssueRecoverAndReplay(t *testing.T) {
	h, binding, attempt, grant, issued := issueTerminalChallenge(t)
	if issued.Nonce == "" || issued.GrantID != grant.ID || issued.AttemptID != attempt.ID || issued.HolderID != binding.PlanID {
		t.Fatalf("challenge not bound: %+v", issued)
	}
	got, err := h.s.GetOfflineTerminalChallenge(h.ctx(), binding.PlanID)
	if err != nil || got != issued {
		t.Fatalf("recover: %+v %v", got, err)
	}
	got, err = h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
		"fleet-verifier", "management-observer")
	if err != nil || got != issued {
		t.Fatalf("replay changed challenge: %+v %v", got, err)
	}
	if _, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
		"different", "management-observer"); !errors.Is(err, ErrOfflineTerminalChallengeConflict) {
		t.Fatalf("changed verifier accepted: %v", err)
	}
	if _, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, binding.PlanID,
		"fleet-verifier", "management-observer"); !errors.Is(err, offline.ErrTerminalAttestation) {
		t.Fatalf("different grant accepted: %v", err)
	}
	if _, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, binding.PlanID, grant.ID,
		"fleet-verifier", "management-observer"); !errors.Is(err, offline.ErrTerminalAttestation) {
		t.Fatalf("different attempt accepted: %v", err)
	}
}

func TestOfflineTerminalChallengePartialRestoreRefused(t *testing.T) {
	h, binding, _, _, issued := issueTerminalChallenge(t)
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineTerminalChallengeEventKey(binding.PlanID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetOfflineTerminalChallenge(h.ctx(), binding.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("missing event accepted: %v", err)
	}
	if _, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, issued.AttemptID, issued.GrantID,
		issued.VerifierSubject, issued.ObserverSubject); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("partial restore reissued: %v", err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineTerminalChallengeKey(binding.PlanID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetOfflineTerminalChallenge(h.ctx(), binding.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("event-only restore accepted: %v", err)
	}
}

func TestOfflineTerminalChallengeTrustRotationRefusesReplay(t *testing.T) {
	h, binding, _, _, issued := issueTerminalChallenge(t)
	trust := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: 2,
		ObserverSubject: "management-observer", KeyID: "observer-key-2",
		PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))}
	raw, err := json.Marshal(trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetOfflineTerminalChallenge(h.ctx(), binding.PlanID); !errors.Is(err, ErrOfflineTerminalChallengeConflict) {
		t.Fatalf("rotated trust read accepted: %v", err)
	}
	if _, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, issued.AttemptID, issued.GrantID,
		issued.VerifierSubject, issued.ObserverSubject); !errors.Is(err, ErrOfflineTerminalChallengeConflict) {
		t.Fatalf("rotated trust replay accepted: %v", err)
	}
}

func TestOfflineTerminalChallengeStaleFenceAndEpochRefused(t *testing.T) {
	for _, kind := range []string{"fence", "epoch", "abandoned", "other-lane"} {
		t.Run(kind, func(t *testing.T) {
			h, binding, attempt, grant, _ := consumedOfflineGrant(t)
			installTerminalObserverTrust(t, h)
			switch kind {
			case "fence":
				rows, err := h.s.kv.Get(h.ctx(), h.s.fleetTargetFenceKey(binding.TargetID))
				if err != nil || len(rows.Kvs) != 1 {
					t.Fatal(err)
				}
				var fence v3FleetTargetFence
				if err := json.Unmarshal(rows.Kvs[0].Value, &fence); err != nil {
					t.Fatal(err)
				}
				fence.Generation++
				raw, _ := json.Marshal(fence)
				if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetFenceKey(binding.TargetID), string(raw)); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				if _, err := h.s.kv.Put(h.ctx(), h.s.fleetAuthorityEpochKey(), "999"); err != nil {
					t.Fatal(err)
				}
			case "abandoned":
				if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetAbandonedKey(binding.PlanID), "abandoned"); err != nil {
					t.Fatal(err)
				}
			case "other-lane":
				if _, err := h.s.kv.Put(h.ctx(), h.s.fleetRunnerAttemptPrefix(binding.PlanID)+"other", "occupied"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
				"fleet-verifier", "management-observer"); !errors.Is(err, ErrOfflineTerminalChallengeConflict) {
				t.Fatalf("stale authority accepted: %v", err)
			}
			if _, err := h.s.GetOfflineTerminalChallenge(h.ctx(), binding.PlanID); !errors.Is(err, ErrOfflineTerminalChallengeConflict) {
				t.Fatalf("stale authority read accepted: %v", err)
			}
		})
	}
}

func TestOfflineTerminalChallengeConcurrentIssueSingleNonce(t *testing.T) {
	h, binding, attempt, grant, _ := consumedOfflineGrant(t)
	installTerminalObserverTrust(t, h)
	var got [2]offline.TerminalChallengeV1
	var issueErr [2]error
	var wait sync.WaitGroup
	start := make(chan struct{})
	for i := range got {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			got[i], issueErr[i] = h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
				"fleet-verifier", "management-observer")
		}(i)
	}
	close(start)
	wait.Wait()
	if issueErr[0] != nil || issueErr[1] != nil || got[0] != got[1] {
		t.Fatalf("race did not recover one challenge: %+v %+v %v %v", got[0], got[1], issueErr[0], issueErr[1])
	}
	first := got[0]
	// A second writer cannot overwrite the pair even with a direct stale CAS.
	var err error
	result, err := h.s.kv.Txn(h.ctx()).If(
		clientv3.Compare(clientv3.CreateRevision(h.s.offlineTerminalChallengeKey(binding.PlanID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(h.s.offlineTerminalChallengeEventKey(binding.PlanID)), "=", 0),
	).Then(clientv3.OpPut(h.s.offlineTerminalChallengeKey(binding.PlanID), "competing")).Commit()
	if err != nil || result.Succeeded {
		t.Fatalf("competing challenge wrote: %v %+v", err, result)
	}
	second, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
		"fleet-verifier", "management-observer")
	if err != nil || second != first {
		t.Fatalf("second nonce: %+v %v", second, err)
	}
}

func TestOfflineTerminalChallengePinsActualTargetAliases(t *testing.T) {
	h, binding, attempt, grant, _ := consumedOfflineGrant(t)
	installTerminalObserverTrust(t, h)
	targetKey := h.s.fleetTargetKey(binding.TargetID)
	row, err := h.s.kv.Get(h.ctx(), targetKey)
	if err != nil || len(row.Kvs) != 1 {
		t.Fatal(err)
	}
	var target FleetTarget
	if err := json.Unmarshal(row.Kvs[0].Value, &target); err != nil {
		t.Fatal(err)
	}
	// The challenge must check every alias actually registered to this target,
	// including aliases outside the staging cluster/environment pair.
	extra := "environment:alternate"
	target.Aliases = append(target.Aliases, extra)
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), targetKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetAliasKey(extra), binding.TargetID); err != nil {
		t.Fatal(err)
	}
	challenge, err := h.s.IssueOfflineTerminalChallenge(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
		"fleet-verifier", "management-observer")
	if err != nil || challenge.GrantID != grant.ID {
		t.Fatalf("extra alias rejected: %+v %v", challenge, err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetTargetAliasKey(extra), "other-target"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetOfflineTerminalChallenge(h.ctx(), binding.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("changed alternate alias accepted: %v", err)
	}
}

func TestOfflineTerminalChallengeAliasMutationDuringCAS(t *testing.T) {
	h, binding, attempt, grant, _ := consumedOfflineGrant(t)
	installTerminalObserverTrust(t, h)
	aliasKey := h.s.fleetTargetAliasKey("cluster:tk-staging-nyc3")
	calls := 0
	clock := func() time.Time {
		calls++
		if calls == 2 {
			if _, err := h.s.kv.Put(h.ctx(), aliasKey, "other-target"); err != nil {
				t.Fatal(err)
			}
		}
		return time.Now().UTC()
	}
	if _, err := h.s.issueOfflineTerminalChallengeWithClock(h.ctx(), binding.PlanID, attempt.ID, grant.ID,
		"fleet-verifier", "management-observer", clock); !errors.Is(err, ErrOfflineTerminalChallengeConflict) {
		t.Fatalf("alias changed after snapshot accepted: %v", err)
	}
	if calls != 2 {
		t.Fatalf("clock calls = %d", calls)
	}
	if _, err := h.s.kv.Get(h.ctx(), h.s.offlineTerminalChallengeKey(binding.PlanID)); err != nil {
		t.Fatal(err)
	}
}
