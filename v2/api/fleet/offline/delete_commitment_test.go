package offline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func deleteInputs(t *testing.T) (Binding, Attempt, EffectGrant, time.Time) {
	t.Helper()
	b := goodBinding()
	b.SchemaVersion = BindingSchemaV2
	b.CandidateSHA256 = strings.Repeat("1", 64)
	ids := []string{"101", "102", WorkDropletID, "104", "105", "106", "107", "108"}
	for i, name := range workNodeNames {
		b.ProviderNodes = append(b.ProviderNodes, ProviderNode{Name: name, ProviderID: ids[i]})
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	now := b.CreatedAt.Add(time.Minute)
	a := Attempt{SchemaVersion: AttemptSchema, ID: "22222222-2222-4222-8222-222222222222", PlanID: b.PlanID,
		BindingSHA256: DeleteCommitmentSHA256(encoded), PrincipalSubject: b.RunnerPrincipalSubject,
		PrincipalTokenID: "token-1", PrincipalDeviceID: "device-1", TargetID: b.TargetID,
		FenceGeneration: b.FenceGeneration, Status: "queued", CurrentPhase: "prechange_verified",
		CreatedAt: b.CreatedAt.Add(10 * time.Second)}
	consumed := now.Add(-time.Second)
	g := EffectGrant{SchemaVersion: EffectGrantSchema, ID: "33333333-3333-4333-8333-333333333333",
		PlanID: b.PlanID, AttemptID: a.ID, BindingSHA256: a.BindingSHA256, TargetID: b.TargetID,
		FenceGeneration: b.FenceGeneration, AuthorityEpoch: 2, SourceCommit: b.SourceCommit,
		SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256, Backend: b.Backend,
		ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName, DropletID: b.DropletID,
		PrechangeSHA256: strings.Repeat("2", 64), IndependentlyObservedSHA256: strings.Repeat("3", 64),
		Phase: "consumed", IssuedAt: now.Add(-2 * time.Second), ExpiresAt: now.Add(58 * time.Second), ConsumedAt: &consumed}
	return b, a, g, now
}

func TestDeleteCommitmentCanonicalAndNoRetry(t *testing.T) {
	b, a, g, now := deleteInputs(t)
	c, raw, err := NewDeleteCommitment(b, a, g,
		"44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555",
		"66666666-6666-4666-8666-666666666666", now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeDeleteCommitment(raw)
	if err != nil || got != c || c.RetryPolicy != DeleteRetryPolicy || c.RuntimeInstanceID == "" {
		t.Fatalf("commitment round trip: %+v %v", got, err)
	}
	if !strings.Contains(string(raw), `"retryPolicy":"never-reissue-after-commit"`) ||
		strings.Contains(string(raw), "providerOperationId") {
		t.Fatal("commitment lacks no-retry rule or models a nonexistent provider operation")
	}
	changed := a
	changed.PrincipalDeviceID = "other-device"
	if changedBytes, err := json.Marshal(changed); err != nil || DeleteCommitmentSHA256(changedBytes) == c.AttemptSHA256 {
		t.Fatal("attempt digest omitted runner device identity")
	}
	changedGrant := g
	changedGrant.IndependentlyObservedSHA256 = strings.Repeat("4", 64)
	if changedBytes, err := json.Marshal(changedGrant); err != nil || DeleteCommitmentSHA256(changedBytes) == c.EffectGrantSHA256 {
		t.Fatal("grant digest omitted independent observation")
	}
	if _, err := DecodeDeleteCommitment(append([]byte(" "), raw...)); err == nil {
		t.Fatal("accepted noncanonical bytes")
	}
}

func TestDeleteCommitmentRejectsChangedAuthorityInputs(t *testing.T) {
	b, a, g, now := deleteInputs(t)
	root, execution, runtime := "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"
	for name, mutate := range map[string]func(*Binding, *Attempt, *EffectGrant){
		"legacy":         func(b *Binding, _ *Attempt, _ *EffectGrant) { b.SchemaVersion = BindingSchema },
		"plan":           func(b *Binding, _ *Attempt, _ *EffectGrant) { b.SavedPlanSHA256 = strings.Repeat("9", 64) },
		"backend":        func(_ *Binding, _ *Attempt, g *EffectGrant) { g.Backend.Serial++ },
		"grant phase":    func(_ *Binding, _ *Attempt, g *EffectGrant) { g.Phase = "issued" },
		"runner":         func(_ *Binding, a *Attempt, _ *EffectGrant) { a.PrincipalSubject = "other" },
		"fence":          func(_ *Binding, _ *Attempt, g *EffectGrant) { g.FenceGeneration++ },
		"attempt":        func(_ *Binding, a *Attempt, _ *EffectGrant) { a.ID = "77777777-7777-4777-8777-777777777777" },
		"attempt status": func(_ *Binding, a *Attempt, _ *EffectGrant) { a.Status = "running" },
		"attempt phase":  func(_ *Binding, a *Attempt, _ *EffectGrant) { a.CurrentPhase = "prechange" },
		"attempt time":   func(_ *Binding, a *Attempt, _ *EffectGrant) { a.CreatedAt = time.Time{} },
		"grant evidence": func(_ *Binding, _ *Attempt, g *EffectGrant) { g.IndependentlyObservedSHA256 = "" },
		"grant time": func(_ *Binding, _ *Attempt, g *EffectGrant) {
			consumed := g.IssuedAt.Add(-time.Second)
			g.ConsumedAt = &consumed
		},
	} {
		t.Run(name, func(t *testing.T) {
			bb, aa, gg := b, a, g
			mutate(&bb, &aa, &gg)
			if _, _, err := NewDeleteCommitment(bb, aa, gg, root, execution, runtime, now); err == nil {
				t.Fatal("accepted changed authority identity")
			}
		})
	}
	if _, _, err := NewDeleteCommitment(b, a, g, root, execution, "", now); err == nil {
		t.Fatal("accepted missing supervised runtime")
	}
	if _, _, err := NewDeleteCommitment(b, a, g, root, execution, runtime, g.ExpiresAt); err == nil {
		t.Fatal("accepted expired grant")
	}
}
