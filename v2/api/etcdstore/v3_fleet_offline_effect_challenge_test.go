package etcdstore

import (
	"errors"
	"testing"

	"norn/v2/api/fleet/offline"
)

func TestOfflineEffectChallengeIssueAndReplay(t *testing.T) {
	h, b, a, _ := offlineEffectFixture(t)
	installTerminalObserverTrust(t, h)
	c, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, "management-observer")
	if err != nil {
		t.Fatal(err)
	}
	if c.PlanID != b.PlanID || c.AttemptID != a.ID || c.Purpose != offline.EffectIssuePurpose || c.Nonce == "" {
		t.Fatalf("unbound challenge: %+v", c)
	}
	recovered, err := h.s.GetOfflineEffectChallenge(h.ctx(), b.PlanID, offline.EffectIssuePurpose)
	if err != nil || recovered != c {
		t.Fatalf("readback: %+v %v", recovered, err)
	}
	replay, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, "management-observer")
	if err != nil || replay != c {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, "wrong-observer"); !errors.Is(err, offline.ErrEffectAttestation) {
		t.Fatalf("wrong observer accepted: %v", err)
	}
}

func TestOfflineEffectChallengePartialRestoreAndFence(t *testing.T) {
	for _, kind := range []string{"partial", "fence"} {
		t.Run(kind, func(t *testing.T) {
			h, b, a, _ := offlineEffectFixture(t)
			installTerminalObserverTrust(t, h)
			if kind == "partial" {
				if _, err := h.s.kv.Put(h.ctx(), h.s.offlineEffectChallengeEventKey(b.PlanID, offline.EffectIssuePurpose), "orphan"); err != nil {
					t.Fatal(err)
				}
				if _, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, "management-observer"); !errors.Is(err, ErrOfflineAttemptCorrupt) {
					t.Fatalf("orphan accepted: %v", err)
				}
			} else {
				if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetTargetFenceKey(b.TargetID)); err != nil {
					t.Fatal(err)
				}
				if _, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, "management-observer"); err == nil {
					t.Fatal("missing fence accepted")
				}
			}
		})
	}
}

func TestOfflineEffectIssueChallengeFencesLegacyGrant(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	installTerminalObserverTrust(t, h)
	if _, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectIssuePurpose, "management-observer"); err != nil {
		t.Fatal(err)
	}
	observation := offlineEffectObservation(b, a, now)
	if _, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, offlineEffectIdentity(b, a), now,
		offlineTestReobserver{observation: observation}); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("legacy unsigned issuance crossed signed challenge: %v", err)
	}
}

func TestOfflineEffectConsumeChallengeBindsCurrentGrant(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	observation := offlineEffectObservation(b, a, now)
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, offlineEffectIdentity(b, a), now,
		offlineTestReobserver{observation: observation})
	if err != nil {
		t.Fatal(err)
	}
	installTerminalObserverTrust(t, h)
	challenge, err := h.s.IssueOfflineEffectChallenge(h.ctx(), b.PlanID, a.ID, offline.EffectConsumePurpose, "management-observer")
	if err != nil || challenge.GrantID != grant.ID || challenge.GrantSHA256 == "" {
		t.Fatalf("consume challenge: %+v %v", challenge, err)
	}
	recovered, err := h.s.GetOfflineEffectChallenge(h.ctx(), b.PlanID, offline.EffectConsumePurpose)
	if err != nil || recovered != challenge {
		t.Fatalf("consume readback: %+v %v", recovered, err)
	}
	if _, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, offlineEffectIdentity(b, a), now,
		offlineBackendReadFor(b)); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("legacy unsigned consume crossed signed challenge: %v", err)
	}
}
