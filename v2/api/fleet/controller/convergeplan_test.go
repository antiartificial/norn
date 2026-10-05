package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
)

// TestConvergePlanFingerprintsUnchanged pins that the converge extension leaves
// an existing capacity plan's canonical bytes and digest byte-identical. The
// golden digest is the one executor's TestConvergeExtensionKeepsCapacityPlanDigest
// pins; it must never change.
func TestConvergePlanFingerprintsUnchanged(t *testing.T) {
	plan := fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: "00000000-0000-4000-8000-000000000001",
		Cluster: "staging", Pool: "workers",
		Current:  fleet.NodePool{Size: "s-2vcpu-4gb", Min: 1, Desired: 2, Max: 4},
		Proposed: fleet.NodePool{Size: "s-2vcpu-4gb", Min: 1, Desired: 3, Max: 4},
		Action:   "scale", Strategy: "rolling", SourceDigest: "sha256:" + strings.Repeat("1", 64),
		Findings: []fleet.Finding{{Severity: "info", Code: "fleet.plan.example", Field: "proposed", Message: "m"}}}
	base, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalConvergePlan(&ConvergeCapacityPlan{CapacityPlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base, canonical) {
		t.Fatalf("converge wrapper changed canonical bytes:\n%s\n%s", base, canonical)
	}
	sum := sha256.Sum256(canonical)
	if got := hex.EncodeToString(sum[:]); got != "8261b6e37a3bdd3cbf1db505e920cbbc276eb0a13f89086703cc87aae4b5ccc2" {
		t.Fatalf("canonical digest = %s", got)
	}
}

func buildForTest(t *testing.T, class, key string) *ConvergeCapacityPlan {
	t.Helper()
	plan, err := BuildConvergePlan(ConvergePlanInput{Resource: "staging", Cluster: "staging", Repository: "acme/norn-fleet",
		CommitSHA: shaB, LastApplied: shaA, PathClass: class, Generation: 2, SigningKey: key,
		NewID: func() string { return "00000000-0000-4000-8000-000000000009" }})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestConvergePlanBuilderSignsAndVerifies(t *testing.T) {
	plan := buildForTest(t, executor.PathClassExecutable, "key-1")
	if plan.Action != executor.ActionConverge || !plan.Converge.RequiresApproval || plan.Converge.Validate() != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if !VerifyConvergePlan(plan, []string{"old", "key-1"}) || VerifyConvergePlan(plan, []string{"other"}) || VerifyConvergePlan(plan, nil) {
		t.Fatal("signature verification wrong")
	}
	if !strings.HasPrefix(plan.Digest, "sha256:") || plan.PlanSHA256() != strings.TrimPrefix(plan.Digest, "sha256:") {
		t.Fatalf("digest %q", plan.Digest)
	}
	// Tampering with the converge object breaks the digest.
	tampered := *plan
	c := *plan.Converge
	c.CommitSHA = shaC
	tampered.Converge = &c
	if VerifyConvergePlan(&tampered, []string{"key-1"}) {
		t.Fatal("tampered converge object verified")
	}
	// The payload round-trips through JSON (the operation payload) unchanged.
	encoded, _ := json.Marshal(plan)
	var back ConvergeCapacityPlan
	if err := json.Unmarshal(encoded, &back); err != nil || !VerifyConvergePlan(&back, []string{"key-1"}) {
		t.Fatalf("round trip: %v", err)
	}
	if !strings.Contains(string(encoded), `"converge":{`) {
		t.Fatalf("converge object missing: %s", encoded)
	}
	if config := buildForTest(t, executor.PathClassConfig, ""); config.Converge.RequiresApproval || config.Signature != "" {
		t.Fatalf("config plan: %+v", config)
	}
}

func TestConvergePlanBuilderRejectsInertAndBadShape(t *testing.T) {
	for _, in := range []ConvergePlanInput{
		{Resource: "r", Cluster: "c", Repository: "x", CommitSHA: shaB, PathClass: executor.PathClassInert, Generation: 1},
		{Resource: "r", Cluster: "", Repository: "x", CommitSHA: shaB, PathClass: executor.PathClassConfig, Generation: 1},
		{Resource: "r", Cluster: "c", Repository: "x", CommitSHA: "abc", PathClass: executor.PathClassConfig, Generation: 1},
		{Resource: "r", Cluster: "c", Repository: "x", CommitSHA: shaB, PathClass: executor.PathClassConfig, Generation: 0},
	} {
		if _, err := BuildConvergePlan(in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
}

// TestConvergePlanGoldenDigest pins the converge payload's canonical bytes so
// the etcd recorder (E6) and admission agree with this builder.
func TestConvergePlanGoldenDigest(t *testing.T) {
	plan := buildForTest(t, executor.PathClassExecutable, "key-1")
	if plan.Digest != "sha256:692458fcd45d2b69014cbec7686d68c6ee458ca3bc47e8ed11a901a3a948ea5e" {
		t.Fatalf("digest = %s", plan.Digest)
	}
}

// The first converge (no last applied commit) always requires approval, even
// for a config-only class, so the stored flag admission reads
// (requireFleetConvergeApprovalTx) agrees with ConvergeNeedsApproval.
func TestConvergePlanFirstConvergeRequiresApproval(t *testing.T) {
	for _, class := range []string{executor.PathClassConfig, executor.PathClassExecutable} {
		plan, err := BuildConvergePlan(ConvergePlanInput{Resource: "staging", Cluster: "staging", Repository: "acme/norn-fleet",
			CommitSHA: shaB, PathClass: class, Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Converge.RequiresApproval || plan.Converge.Validate() != nil {
			t.Fatalf("%s first converge: requiresApproval=%v", class, plan.Converge.RequiresApproval)
		}
		if !ConvergeNeedsApproval(ConvergePlanFacts{RequiresApproval: plan.Converge.RequiresApproval, BaseCommitSHA: plan.Converge.BaseCommitSHA}) {
			t.Fatalf("%s first converge: read side disagrees", class)
		}
	}
	if buildForTest(t, executor.PathClassConfig, "k").Converge.RequiresApproval {
		t.Fatal("a config range over a last applied commit must auto-apply")
	}
}
