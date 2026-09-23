package nomad

import "testing"

func TestReviewDatabaseDeliveryCannotRepointLiveJob(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	current := DatabaseVariableItems(map[string]string{"primary": "postgresql://app@new-target:5432/shop"})
	stale := DatabaseVariableItems(map[string]string{"primary": "postgresql://app@old-target:5432/shop"})
	if err := client.PutDatabaseVariable("west", "shop", current); err != nil {
		t.Fatal(err)
	}
	// An old operation reaches delivery after a newer target was published.
	// Reading the latest CAS index does not confer authority to replace it.
	_ = client.PutDatabaseVariable("west", "shop", stale)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.variables[DatabaseVariablePath("shop")].Items[DatabaseItemKey("primary")] != current[DatabaseItemKey("primary")] {
		t.Fatal("stale delivery repointed material used by an already running job")
	}
}

func TestReviewCandidateDeliveryDoesNotRepointCurrentAllocations(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	current := DatabaseVariableItems(map[string]string{"primary": "postgresql://app@current-target:5432/shop"})
	candidate := DatabaseVariableItems(map[string]string{"primary": "postgresql://app@candidate-target:5432/shop"})
	if err := client.DeliverDatabaseVariable("west", "shop", current, 1); err != nil {
		t.Fatal(err)
	}
	// Existing allocations still reference this path. Preparing candidate
	// material is not authorization to cut them over before rollout readiness.
	_ = client.DeliverDatabaseVariable("west", "shop", candidate, 2)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.variables[DatabaseVariablePath("shop")].Items[DatabaseItemKey("primary")] != current[DatabaseItemKey("primary")] {
		t.Fatal("candidate publication changed material referenced by current allocations before cutover")
	}
}
