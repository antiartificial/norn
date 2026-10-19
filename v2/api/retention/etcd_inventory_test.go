package retention

import "testing"

func TestOfflineEtcdPayloadInventory(t *testing.T) {
	want := map[string]string{
		"/v3/fleet-offline-dispatches/":          ClassHotEvidence,
		"/v3/fleet-offline-owner-consumptions/":  ClassCurrentState,
		"/v3/fleet-offline-attempts/":            ClassHotEvidence,
		"/v3/fleet-offline-checkpoints/":         ClassHotEvidence,
		"/v3/fleet-offline-checkpoint-events/":   ClassHotEvidence,
		"/v3/fleet-offline-effect-grants/":       ClassCurrentState,
		"/v3/fleet-offline-effect-grant-events/": ClassHotEvidence,
	}
	for _, entry := range OfflineEtcdPayloadInventory {
		class, ok := want[entry.Prefix]
		if !ok || class != entry.Class || entry.Holds == "" {
			t.Fatalf("missing or invalid retention decision: %+v", entry)
		}
		delete(want, entry.Prefix)
	}
	if len(want) != 0 {
		t.Fatalf("unclassified offline etcd keys: %v", want)
	}
}
