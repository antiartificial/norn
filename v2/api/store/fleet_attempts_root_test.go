package store

import "testing"

func TestServerOwnedRootAttemptIDInheritsAcrossRetries(t *testing.T) {
	root, err := serverOwnedRootAttemptID(1, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "forged")
	if err != nil || root != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("attempt A root = %q, %v", root, err)
	}
	for _, attempt := range []int{2, 3} {
		got, err := serverOwnedRootAttemptID(attempt, "different-client-id", root)
		if err != nil || got != root {
			t.Fatalf("attempt %d inherited root = %q, %v", attempt, got, err)
		}
	}
	if _, err := serverOwnedRootAttemptID(2, "id", ""); err == nil {
		t.Fatal("missing persisted root was accepted")
	}
}
