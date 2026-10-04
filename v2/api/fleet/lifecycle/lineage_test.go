package lifecycle

import (
	"testing"

	"norn/v2/api/fleet"
)

func TestValidateLineageOrderInsensitive(t *testing.T) {
	t.Parallel()
	root := fleet.RunnerAttempt{ID: "a1", Attempt: 1, RootAttemptID: "a1"}
	retry := fleet.RunnerAttempt{ID: "a2", Attempt: 2, RootAttemptID: "a1", RetryOf: "a1"}
	secondRetry := fleet.RunnerAttempt{ID: "a3", Attempt: 3, RootAttemptID: "a1", RetryOf: "a2"}

	ascending := []fleet.RunnerAttempt{root, retry, secondRetry}
	if err := ValidateLineage(ascending); err != nil {
		t.Fatalf("ascending valid chain: %v", err)
	}

	descending := []fleet.RunnerAttempt{secondRetry, retry, root}
	if err := ValidateLineage(descending); err != nil {
		t.Fatalf("descending valid chain: %v", err)
	}
	if descending[0].ID != "a3" || descending[2].ID != "a1" {
		t.Fatalf("ValidateLineage mutated caller's slice order: %v", descending)
	}

	broken := []fleet.RunnerAttempt{root, {ID: "a2", Attempt: 2, RootAttemptID: "a1", RetryOf: "wrong"}}
	if err := ValidateLineage(broken); err == nil {
		t.Fatal("expected an error for a broken retry chain")
	}

	badRoot := []fleet.RunnerAttempt{{ID: "a1", Attempt: 2, RootAttemptID: "a1"}}
	if err := ValidateLineage(badRoot); err == nil {
		t.Fatal("expected an error for a root whose Attempt is not 1")
	}

	if err := ValidateLineage(nil); err != nil {
		t.Fatalf("empty lineage should be valid: %v", err)
	}
}
