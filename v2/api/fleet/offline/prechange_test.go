package offline

import (
	"encoding/json"
	"testing"
	"time"
)

func prechangeFixture(b Binding, at time.Time) map[string]any {
	nodes := []map[string]string{{"name": "app-01", "providerId": "1", "status": "active"}, {"name": "app-02", "providerId": "2", "status": "active"}, {"name": "app-03", "providerId": b.DropletID, "status": "active"}}
	for _, name := range []string{"control-01", "control-02", "control-03", "ingress-01", "ingress-02"} {
		nodes = append(nodes, map[string]string{"name": name, "providerId": name, "status": "active"})
	}
	return map[string]any{"schemaVersion": PrechangeSchema, "observedAt": at, "readOnly": true, "authorizesEffect": false,
		"plan":      map[string]any{"source": map[string]any{"commit": b.SourceCommit, "root": b.Resource}, "savedPlanSHA256": b.SavedPlanSHA256, "effectSHA256": b.EffectSHA256, "tofuVersion": "1.10.6"},
		"freshness": map[string]any{"schemaVersion": "norn.fleet-offline-contraction-freshness/v1", "observedAt": at.Add(-time.Second), "accountUUID": b.ProviderAccountUUID, "backend": b.Backend, "nodes": nodes, "readOnly": true, "authorizesEffect": false},
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
		if want && (summary.SHA256 == "" || summary.ExpiresAt.Before(now)) {
			t.Fatal("incomplete summary")
		}
	}
	check(base, true)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"stale", func(r map[string]any) { r["observedAt"] = now.Add(-6 * time.Minute) }},
		{"source", func(r map[string]any) { r["plan"].(map[string]any)["source"].(map[string]any)["commit"] = "wrong" }},
		{"plan", func(r map[string]any) { r["plan"].(map[string]any)["effectSHA256"] = "wrong" }},
		{"state", func(r map[string]any) { r["freshness"].(map[string]any)["backend"] = Backend{Serial: 23} }},
		{"account", func(r map[string]any) { r["freshness"].(map[string]any)["accountUUID"] = "personal" }},
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
