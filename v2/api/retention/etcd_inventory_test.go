package retention

import "testing"

func TestOfflineEtcdPayloadInventory(t *testing.T) {
	want := map[string]string{
		"/v3/fleet-offline-dispatches/":                         ClassCurrentState,
		"/v3/fleet-offline-owner-consumptions/":                 ClassCurrentState,
		"/v3/fleet-offline-attempts/":                           ClassCurrentState,
		"/v3/fleet-offline-checkpoints/":                        ClassCurrentState,
		"/v3/fleet-offline-checkpoint-events/":                  ClassCurrentState,
		"/v3/fleet-offline-effect-grants/":                      ClassCurrentState,
		"/v3/fleet-offline-effect-grant-events/":                ClassCurrentState,
		"/v3/fleet-offline-terminals/":                          ClassHotEvidence,
		"/v3/fleet-offline-terminal-events/":                    ClassHotEvidence,
		"/v3/fleet-offline-delete-commitments/":                 ClassCurrentState,
		"/v3/fleet-offline-delete-commitment-events/":           ClassCurrentState,
		"/v3/fleet-offline-delete-dispatch-reservations/":       ClassCurrentState,
		"/v3/fleet-offline-delete-dispatch-reservation-events/": ClassCurrentState,
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
