package etcdstore

import (
	"errors"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

func TestSignedEffectGrantIssueRecoverAndFencesLegacy(t *testing.T) {
	h, b, a, c, envelope, now := signedEffectIssueFixture(t)
	if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, offlineEffectIdentity(b, a), now,
		offlineTestReobserver{observation: offlineEffectObservation(b, a, now)}); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("legacy issuance crossed signed challenge: %v", err)
	}
	grant, err := h.s.issueOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, c.ID, offlineEffectIdentity(b, a), func() time.Time { return now.Add(time.Millisecond) })
	if err != nil {
		t.Fatal(err)
	}
	if grant.Phase != "issued" || grant.ID == "" || grant.IndependentlyObservedSHA256 == "" || grant.ExpiresAt.After(c.ExpiresAt) {
		t.Fatalf("grant not bounded: %+v", grant)
	}
	recovered, err := h.s.GetOfflineSignedEffectGrant(h.ctx(), b.PlanID, a.ID, c.ID)
	if err != nil || recovered != grant {
		t.Fatalf("recover: %+v %v", recovered, err)
	}
	replay, err := h.s.issueOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, c.ID, offlineEffectIdentity(b, a), func() time.Time { return c.ExpiresAt.Add(time.Hour) })
	if err != nil || replay != grant {
		t.Fatalf("exact replay: %+v %v", replay, err)
	}
	wrong := offlineEffectIdentity(b, a)
	wrong.DeviceID = "another-device"
	if _, err := h.s.issueOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, c.ID, wrong,
		func() time.Time { return c.ExpiresAt.Add(time.Hour) }); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong-device replay recovered grant: %v", err)
	}
}

func TestSignedEffectGrantRejectsChangedAuthorityAfterAcceptance(t *testing.T) {
	for _, kind := range []string{"fence", "epoch", "alias", "competing-lane"} {
		t.Run(kind, func(t *testing.T) {
			h, b, a, c, envelope, now := signedEffectIssueFixture(t)
			if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope,
				func() time.Time { return now }); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "fence":
				if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetTargetFenceKey(b.TargetID)); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				if _, err := h.s.kv.Put(h.ctx(), h.s.fleetAuthorityEpochKey(), "2"); err != nil {
					t.Fatal(err)
				}
			case "alias":
				if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetTargetAliasKey("environment:staging")); err != nil {
					t.Fatal(err)
				}
			case "competing-lane":
				if _, err := h.s.kv.Put(h.ctx(), h.s.fleetRunnerDispatchKey(b.PlanID), "other"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.s.issueOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, c.ID,
				offlineEffectIdentity(b, a), func() time.Time { return now.Add(time.Millisecond) }); err == nil {
				t.Fatal("changed authority issued grant")
			}
			if _, err := h.s.GetOfflineEffectGrant(h.ctx(), b.PlanID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("grant created despite changed authority: %v", err)
			}
		})
	}
}

func TestSignedEffectGrantRejectsStaleAndWrongIdentity(t *testing.T) {
	h, b, a, c, envelope, now := signedEffectIssueFixture(t)
	if _, err := h.s.submitOfflineEffectAttestationWithClock(h.ctx(), b.PlanID, a.ID, c.Purpose, c.ID, envelope, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	wrong := offlineEffectIdentity(b, a)
	wrong.DeviceID = "another-device"
	if _, err := h.s.issueOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, c.ID, wrong, func() time.Time { return now }); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong device accepted: %v", err)
	}
	if _, err := h.s.issueOfflineEffectGrantFromAcceptedWithClock(h.ctx(), b.PlanID, a.ID, c.ID, offlineEffectIdentity(b, a), func() time.Time { return now.Add(31 * time.Second) }); err == nil {
		t.Fatal("stale acceptance issued grant")
	}
	if _, err := h.s.GetOfflineEffectGrant(h.ctx(), b.PlanID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale attempt issued grant: %v", err)
	}
}
