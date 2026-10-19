package offline

import (
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestV2OwnerSignedTopologyBinding(t *testing.T) {
	b := goodBinding()
	b.SchemaVersion = BindingSchemaV2
	b.CandidateSHA256 = strings.Repeat("1", 64)
	for i, name := range workNodeNames {
		id := strconv.Itoa(i + 1)
		if name == WorkNode {
			id = WorkDropletID
		}
		b.ProviderNodes = append(b.ProviderNodes, ProviderNode{Name: name, ProviderID: id})
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Binding)
	}{
		{"missing candidate", func(v *Binding) { v.CandidateSHA256 = "" }},
		{"missing node", func(v *Binding) { v.ProviderNodes = v.ProviderNodes[:7] }},
		{"duplicate provider id", func(v *Binding) { v.ProviderNodes[1].ProviderID = v.ProviderNodes[0].ProviderID }},
		{"wrong app03", func(v *Binding) { v.ProviderNodes[2].ProviderID = "999999" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := b
			changed.ProviderNodes = append([]ProviderNode(nil), b.ProviderNodes...)
			tc.mutate(&changed)
			if changed.Validate() == nil {
				t.Fatal("invalid v2 topology accepted")
			}
		})
	}
	key := ed25519.NewKeyFromSeed([]byte("01234567890123456789012345678901"))
	b, approval := signedApprovalFor(t, b, key)
	encodedBinding, _ := json.Marshal(b)
	decodedBinding, err := DecodeBinding(encodedBinding)
	if err != nil || !sameProviderNodes(decodedBinding.ProviderNodes, b.ProviderNodes) {
		t.Fatalf("v2 binding roundtrip failed: %v", err)
	}
	now := b.CreatedAt.Add(2 * time.Minute)
	if _, err := VerifySignedApproval(approval, approval.KeyID, key.Public().(ed25519.PublicKey), b, now); err != nil {
		t.Fatal(err)
	}
	if err := b.MatchAttempt(claimOf(b), now); err != nil {
		t.Fatal(err)
	}
	a := Attempt{ID: "attempt-1", PlanID: b.PlanID, TargetID: b.TargetID, FenceGeneration: b.FenceGeneration, BindingSHA256: strings.Repeat("f", 64)}
	fixture := prechangeFixture(b, now.Add(-time.Minute))
	nodes := fixture["freshness"].(map[string]any)["nodes"].([]map[string]string)
	for i, row := range b.ProviderNodes {
		nodes[i]["providerId"] = row.ProviderID
	}
	raw, _ := json.Marshal(fixture)
	summary, err := ValidatePrechangeReceipt(raw, b, a, now)
	if err != nil || !summary.ProviderTopologyVerified || summary.BindingSHA256 != a.BindingSHA256 {
		t.Fatalf("v2 topology binding refused: %v %+v", err, summary)
	}
	changed := b
	changed.ProviderNodes = append([]ProviderNode(nil), b.ProviderNodes...)
	changed.ProviderNodes[0].ProviderID = "999999"
	if _, err := VerifySignedApproval(approval, approval.KeyID, key.Public().(ed25519.PublicKey), changed, now); err == nil {
		t.Fatal("owner signature accepted changed survivor")
	}
	if _, err := ValidatePrechangeReceipt(raw, changed, a, now); err == nil {
		t.Fatal("receipt accepted changed signed survivor")
	}
	badClaim := claimOf(b)
	badClaim.ProviderNodes = append([]ProviderNode(nil), b.ProviderNodes...)
	badClaim.ProviderNodes[0].ProviderID = "999999"
	if b.MatchAttempt(badClaim, now) == nil {
		t.Fatal("attempt accepted changed survivor")
	}
	fixture["plan"].(map[string]any)["candidateSHA256"] = strings.Repeat("2", 64)
	raw, _ = json.Marshal(fixture)
	if _, err := ValidatePrechangeReceipt(raw, b, a, now); err == nil {
		t.Fatal("receipt accepted changed candidate")
	}
}

func prechangeFixture(b Binding, at time.Time) map[string]any {
	nodes := []map[string]string{{"name": "app-01", "providerId": "1", "status": "active"}, {"name": "app-02", "providerId": "2", "status": "active"}, {"name": "app-03", "providerId": b.DropletID, "status": "active"}}
	for i, name := range []string{"control-01", "control-02", "control-03", "ingress-01", "ingress-02"} {
		nodes = append(nodes, map[string]string{"name": name, "providerId": strconv.Itoa(i + 4), "status": "active"})
	}
	return map[string]any{"schemaVersion": PrechangeSchema, "observedAt": at, "readOnly": true, "authorizesEffect": false,
		"plan":      map[string]any{"candidateSHA256": strings.Repeat("1", 64), "source": map[string]any{"commit": b.SourceCommit, "root": b.Resource, "lockfileSHA256": strings.Repeat("2", 64)}, "provider": map[string]any{"address": "registry.opentofu.org/digitalocean/digitalocean", "version": "2.100.0", "archiveSHA256": reviewedProviderArchiveSHA256}, "savedPlanSHA256": b.SavedPlanSHA256, "effectSHA256": b.EffectSHA256, "tofuVersion": "1.10.6"},
		"freshness": map[string]any{"schemaVersion": "norn.fleet-offline-contraction-freshness/v1", "observedAt": at.Add(-time.Second), "accountUUID": b.ProviderAccountUUID, "candidateSHA256": strings.Repeat("1", 64), "vpcUUID": workVPCUUID, "backend": b.Backend, "nodes": nodes, "readOnly": true, "authorizesEffect": false},
		"workload":  map[string]any{"job": "hello-norn-mysql", "namespace": "default", "runningAllocationIDs": []string{"alloc1", "alloc2"}, "survivingClientNames": []string{"app-01", "app-02"}}}
}

func TestPrechangeReceiptExactFreshBinding(t *testing.T) {
	b := goodBinding()
	now := b.CreatedAt.Add(2 * time.Minute)
	a := Attempt{ID: "attempt-1", PlanID: b.PlanID, TargetID: b.TargetID, FenceGeneration: b.FenceGeneration, BindingSHA256: "binding"}
	base := prechangeFixture(b, now.Add(-time.Minute))
	check := func(row map[string]any, want bool) {
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := ValidatePrechangeReceipt(raw, b, a, now)
		if (err == nil) != want {
			t.Fatalf("accepted=%v want=%v: %v", err == nil, want, err)
		}
		if want && (summary.SHA256 == "" || summary.ExpiresAt.Before(now) || summary.ProviderTopologyVerified) {
			t.Fatal("incomplete summary")
		}
	}
	check(base, true)
	// Survivor IDs are not present in the signed reservation. Their alteration
	// can be noted, but this parser must never turn it into topology proof.
	changedRaw, _ := json.Marshal(base)
	var untrusted map[string]any
	_ = json.Unmarshal(changedRaw, &untrusted)
	untrusted["freshness"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["providerId"] = "99999999"
	check(untrusted, true)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"stale", func(r map[string]any) { r["observedAt"] = now.Add(-6 * time.Minute) }},
		{"source", func(r map[string]any) { r["plan"].(map[string]any)["source"].(map[string]any)["commit"] = "wrong" }},
		{"plan", func(r map[string]any) { r["plan"].(map[string]any)["effectSHA256"] = "wrong" }},
		{"state", func(r map[string]any) { r["freshness"].(map[string]any)["backend"] = Backend{Serial: 23} }},
		{"account", func(r map[string]any) { r["freshness"].(map[string]any)["accountUUID"] = "personal" }},
		{"candidate", func(r map[string]any) { r["freshness"].(map[string]any)["candidateSHA256"] = strings.Repeat("3", 64) }},
		{"vpc", func(r map[string]any) { r["freshness"].(map[string]any)["vpcUUID"] = "another-vpc" }},
		{"provider", func(r map[string]any) {
			r["plan"].(map[string]any)["provider"].(map[string]any)["archiveSHA256"] = strings.Repeat("4", 64)
		}},
		{"node", func(r map[string]any) {
			r["freshness"].(map[string]any)["nodes"].([]any)[2].(map[string]any)["providerId"] = "wrong"
		}},
		{"placement", func(r map[string]any) {
			r["workload"].(map[string]any)["survivingClientNames"] = []string{"app-01", "app-03"}
		}},
		{"effect", func(r map[string]any) { r["authorizesEffect"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var changed map[string]any
			_ = json.Unmarshal(raw, &changed)
			tc.mutate(changed)
			check(changed, false)
		})
	}
	a.FenceGeneration++
	check(base, false)
}
