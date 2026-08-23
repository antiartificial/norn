package handler

import (
	"strings"
	"testing"
)

func TestRecoveryDrillEvidenceIsBounded(t *testing.T) {
	if got := validateRecoveryDrillEvidence(map[string]string{"receipt": "snapshot-123"}); got != "" {
		t.Fatalf("valid evidence rejected: %s", got)
	}
	if got := validateRecoveryDrillEvidence(map[string]string{"receipt": strings.Repeat("x", 1025)}); got == "" {
		t.Fatal("oversized evidence was accepted")
	}
	if got := validateRecoveryDrillEvidence(map[string]string{"api_token": "must-not-be-stored"}); got == "" {
		t.Fatal("secret-bearing evidence key was accepted")
	}
}

func TestRequiredRecoveryDrillsAreAllowlisted(t *testing.T) {
	for _, kind := range requiredRecoveryDrillKinds {
		if !recoveryDrillKinds[kind] {
			t.Fatalf("required drill %q is not accepted", kind)
		}
	}
}
