package runtime

import "testing"

func TestParseBackendRequiresExplicitSelection(t *testing.T) {
	if _, err := ParseBackend("auto"); err == nil {
		t.Fatal("auto selection must be rejected")
	}
	if got, err := ParseBackend("apple-container"); err != nil || got != AppleContainer {
		t.Fatalf("apple-container = %q, %v", got, err)
	}
}
