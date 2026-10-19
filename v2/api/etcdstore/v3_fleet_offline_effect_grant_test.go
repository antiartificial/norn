package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
)

type offlineTestReobserver struct {
	observation offline.EffectObservation
	err         error
}

func (o offlineTestReobserver) reobserveEffect(context.Context, offline.Binding, offline.Attempt) (offline.EffectObservation, error) {
	return o.observation, o.err
}

func offlineEffectFixture(t *testing.T) (*etcdExecutorHarness, offline.Binding, offline.Attempt, time.Time) {
	t.Helper()
	h, target := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	planID := uuid.NewString()
	offlineTestPlan(h, planID)
	b := offlineTestBinding(planID, target, now)
	b.SchemaVersion = offline.BindingSchemaV2
	b.CandidateSHA256 = strings.Repeat("1", 64)
	b.ProviderNodes = []offline.ProviderNode{
		{Name: "app-01", ProviderID: "101"}, {Name: "app-02", ProviderID: "102"},
		{Name: "app-03", ProviderID: offline.WorkDropletID}, {Name: "control-01", ProviderID: "201"},
		{Name: "control-02", ProviderID: "202"}, {Name: "control-03", ProviderID: "203"},
		{Name: "ingress-01", ProviderID: "301"}, {Name: "ingress-02", ProviderID: "302"},
	}
	b, approval := offlineTestApproval(t, b, now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), b, approval, now); err != nil {
		t.Fatal(err)
	}
	a, err := h.s.AdmitOfflineAttempt(h.ctx(), offlineAttemptClaim(b), b.RunnerPrincipalSubject, uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	// Populate a validated checkpoint-shaped fixture; production writes it only
	// through RecordOfflineCheckpoint and its strict receipt parser.
	cp := offline.Checkpoint{SchemaVersion: offline.CheckpointSchema, PlanID: planID, AttemptID: a.ID,
		Sequence: 1, Phase: "prechange_observed", EvidenceSHA256: strings.Repeat("2", 64),
		AuthorityEpoch: 1, FenceGeneration: b.FenceGeneration, RecordedAt: now,
		Prechange: &offline.PrechangeSummary{SchemaVersion: offline.PrechangeSchema, SHA256: strings.Repeat("2", 64),
			ObservedAt: now, ExpiresAt: now.Add(5 * time.Minute), BindingSHA256: a.BindingSHA256, ProviderTopologyVerified: true}}
	raw, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineCheckpointKey(planID), string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineCheckpointEventKey(planID, 1), string(raw)); err != nil {
		t.Fatal(err)
	}
	return h, b, a, now
}

func offlineEffectObservation(b offline.Binding, a offline.Attempt, now time.Time) offline.EffectObservation {
	return offline.EffectObservation{PlanID: b.PlanID, AttemptID: a.ID, BindingSHA256: a.BindingSHA256,
		TargetID: b.TargetID, SourceCommit: b.SourceCommit, SavedPlanSHA256: b.SavedPlanSHA256,
		EffectSHA256: b.EffectSHA256, Backend: b.Backend, ProviderAccountUUID: b.ProviderAccountUUID,
		NodeName: b.NodeName, DropletID: b.DropletID, FenceGeneration: b.FenceGeneration, AuthorityEpoch: 1,
		ObservedAt: now, EvidenceSHA256: strings.Repeat("3", 64), TrustedOrigin: true, ProviderExact: true,
		LatestBackendExact: true, AllNamespacesInspected: true, App03Empty: true, SurvivingPlacement: true,
		SurvivingHeadroom: true, ServicePassing: true}
}

func TestOfflineEffectGrantFailClosedAndOneUseCAS(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	if _, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, nil); !errors.Is(err, offline.ErrEffectProofMissing) {
		t.Fatalf("nil observer: %v", err)
	}
	o := offlineEffectObservation(b, a, now)
	bad := o
	bad.AllNamespacesInspected = false
	if _, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, offlineTestReobserver{observation: bad}); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("missing all-namespace proof: %v", err)
	}
	bad = o
	bad.Backend.RawSHA256 = strings.Repeat("4", 64)
	if _, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, offlineTestReobserver{observation: bad}); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("stale backend: %v", err)
	}
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, offlineTestReobserver{observation: o})
	if err != nil || grant.Phase != "issued" {
		t.Fatalf("issue: %+v %v", grant, err)
	}
	if _, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, offlineTestReobserver{observation: o}); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("reissue: %v", err)
	}
	if _, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, "wrong", now); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("wrong principal: %v", err)
	}
	consumed, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject, now)
	if err != nil || consumed.Phase != "consumed" || consumed.ConsumedAt == nil {
		t.Fatalf("consume: %+v %v", consumed, err)
	}
	if _, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject, now); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("replay: %v", err)
	}
	rows, err := h.s.kv.Get(h.ctx(), h.s.offlineEffectGrantEventKey(b.PlanID, 2))
	if err != nil || len(rows.Kvs) != 1 {
		t.Fatalf("missing durable consumption event: %v", err)
	}
}

func TestOfflineEffectGrantRejectsCompetingLaneAndStaleEpoch(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	o := offlineEffectObservation(b, a, now)
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(b.PlanID), "competing"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, offlineTestReobserver{observation: o}); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("competing lane: %v", err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(b.PlanID)); err != nil {
		t.Fatal(err)
	}
	o.AuthorityEpoch++
	if _, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, offlineTestReobserver{observation: o}); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("stale epoch: %v", err)
	}
}

func TestOfflineEffectGrantConsumeRejectsCompetingLaneAndCrashShape(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	o := offlineEffectObservation(b, a, now)
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now, offlineTestReobserver{observation: o})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.kv.Put(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(b.PlanID), "competing"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject, now); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("competing lane at consume: %v", err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.fleetGitHubDispatchPreparationKey(b.PlanID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject, grant.ExpiresAt); !errors.Is(err, offline.ErrEffectProofMismatch) {
		t.Fatalf("expired grant: %v", err)
	}
	// Simulate a partial restore: a consumed cursor without its append-only
	// consumption event must be treated as corrupt, never as a fresh grant.
	consumed, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject, now)
	if err != nil {
		t.Fatal(err)
	}
	head, err := json.Marshal(consumed)
	if err != nil {
		t.Fatal(err)
	}
	issuedRows, err := h.s.kv.Get(h.ctx(), h.s.offlineEffectGrantEventKey(b.PlanID, 1))
	if err != nil || len(issuedRows.Kvs) != 1 {
		t.Fatalf("issued event: %v", err)
	}
	if _, err := decodeOfflineEffectGrantPair(head, issuedRows.Kvs[0].Value, nil, b.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("partial restore accepted: %v", err)
	}
}

func TestOfflineEffectGrantPartialRestoreMarksTargetHolderIncomplete(t *testing.T) {
	h, b, a, now := offlineEffectFixture(t)
	grant, err := h.s.issueOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, b.RunnerPrincipalSubject, now,
		offlineTestReobserver{observation: offlineEffectObservation(b, a, now)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.consumeOfflineEffectGrant(h.ctx(), b.PlanID, a.ID, grant.ID, b.RunnerPrincipalSubject, now); err != nil {
		t.Fatal(err)
	}
	facts, err := h.s.loadFleetTargetHolderFacts(h.ctx(), b.PlanID)
	if err != nil || !facts.OfflineReservation || facts.OfflineEvidenceIncomplete {
		t.Fatalf("intact holder: %+v %v", facts, err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineEffectGrantEventKey(b.PlanID, 2)); err != nil {
		t.Fatal(err)
	}
	facts, err = h.s.loadFleetTargetHolderFacts(h.ctx(), b.PlanID)
	if err != nil || !facts.OfflineReservation || !facts.OfflineEvidenceIncomplete {
		t.Fatalf("missing consumption event accepted: %+v %v", facts, err)
	}
	if state, reason, err := h.s.FleetTargetOutcome(h.ctx(), b.TargetID, now); err != nil ||
		state != lifecycle.OccupancyUncertain || reason != lifecycle.ReasonOfflineEvidenceIncomplete {
		t.Fatalf("target outcome missed partial grant restore: %s/%s %v", state, reason, err)
	}
	if _, err := h.s.kv.Delete(h.ctx(), h.s.offlineDispatchKey(b.PlanID)); err != nil {
		t.Fatal(err)
	}
	facts, err = h.s.loadFleetTargetHolderFacts(h.ctx(), b.PlanID)
	if err != nil || !facts.OfflineReservation || !facts.OfflineEvidenceIncomplete {
		t.Fatalf("orphan grant accepted: %+v %v", facts, err)
	}
}
