package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFleetRunnerEnvelopeOmitsAbsentLegacyWorkloadSHA(t *testing.T) {
	encoded, err := json.Marshal(fleetRunnerAttemptEnvelope{ID: "attempt", PlanID: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "workloadSha") {
		t.Fatalf("legacy envelope changed: %s", encoded)
	}
}
