package fleet

import (
	"strings"
	"testing"
)

func TestProtectedApplyTerminalIntentIsCanonicalAndAttemptBound(t *testing.T) {
	prestate := &ProtectedApplyPrestate{ProviderAccountUUID: "account", ProviderTargetID: "droplet-123", ProviderTargetState: "present", Spaces: ProtectedApplyStateSpaces{Bucket: "state", Key: "fleet/terraform.tfstate", Lineage: "lineage", Serial: 7, VersionID: "version", RawSHA256: strings.Repeat("a", 64)}}
	rule := &ProtectedApplyTransitionRule{ProviderTargetState: "present", StateChange: "write"}
	poststate := &ObservedProtectedApplyPoststate{ProviderAccountUUID: "account", ProviderTargetID: "droplet-123", ProviderTargetState: "present", Spaces: ProtectedApplyStateSpaces{Bucket: "state", Key: "fleet/terraform.tfstate", Lineage: "lineage", Serial: 8, VersionID: "version-2", RawSHA256: strings.Repeat("d", 64)}}
	first, err := BuildProtectedApplyTerminalIntent(prestate, rule, poststate, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", strings.Repeat("b", 40), strings.Repeat("c", 64))
	if err != nil || !strings.HasPrefix(first.CanonicalDigest, "sha256:") {
		t.Fatalf("intent=%+v err=%v", first, err)
	}
	second, err := BuildProtectedApplyTerminalIntent(prestate, rule, poststate, first.PlanID, "33333333-3333-4333-8333-333333333333", first.SourceCommit, first.PlanSHA256)
	if err != nil || first.CanonicalDigest == second.CanonicalDigest {
		t.Fatalf("attempt binding missing: first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestProtectedApplyTransitionRejectsInvalidObservedPoststate(t *testing.T) {
	prestate := &ProtectedApplyPrestate{ProviderAccountUUID: "account", ProviderTargetID: "target", ProviderTargetState: "present", Spaces: ProtectedApplyStateSpaces{Bucket: "bucket", Key: "key", Lineage: "lineage", Serial: 7, VersionID: "version", RawSHA256: strings.Repeat("a", 64)}}
	poststate := &ObservedProtectedApplyPoststate{ProviderAccountUUID: "account", ProviderTargetID: "target", ProviderTargetState: "present", Spaces: ProtectedApplyStateSpaces{Bucket: "bucket", Key: "key", Lineage: "lineage", Serial: 7, VersionID: "version-2", RawSHA256: strings.Repeat("b", 64)}}
	if err := ValidateProtectedApplyTransition(prestate, &ProtectedApplyTransitionRule{ProviderTargetState: "present", StateChange: "write"}, poststate); err == nil {
		t.Fatal("write transition accepted unchanged serial")
	}
	poststate.Spaces.Serial, poststate.Spaces.VersionID, poststate.Spaces.RawSHA256 = 8, "version", strings.Repeat("a", 64)
	if err := ValidateProtectedApplyTransition(prestate, &ProtectedApplyTransitionRule{ProviderTargetState: "present", StateChange: "write"}, poststate); err == nil {
		t.Fatal("write transition accepted unchanged version and raw digest")
	}
	if err := ValidateProtectedApplyTransition(prestate, &ProtectedApplyTransitionRule{ProviderTargetState: "present", StateChange: "no-op"}, poststate); err == nil {
		t.Fatal("no-op transition accepted changed state")
	}
	poststate.Spaces.Serial, poststate.Spaces.VersionID, poststate.Spaces.RawSHA256 = 8, "version-2", strings.Repeat("b", 64)
	poststate.Spaces.Bucket = "other"
	if err := ValidateProtectedApplyTransition(prestate, &ProtectedApplyTransitionRule{ProviderTargetState: "present", StateChange: "write"}, poststate); err == nil {
		t.Fatal("transition accepted different bucket")
	}
}

func TestProtectedApplyTerminalIntentRejectsTransitionRuleMutation(t *testing.T) {
	prestate := &ProtectedApplyPrestate{ProviderAccountUUID: "account", ProviderTargetID: "target", ProviderTargetState: "present", Spaces: ProtectedApplyStateSpaces{Bucket: "bucket", Key: "key", Lineage: "lineage", Serial: 7, VersionID: "version", RawSHA256: strings.Repeat("a", 64)}}
	rule := &ProtectedApplyTransitionRule{ProviderTargetState: "present", StateChange: "write"}
	poststate := &ObservedProtectedApplyPoststate{ProviderAccountUUID: "account", ProviderTargetID: "target", ProviderTargetState: "present", Spaces: ProtectedApplyStateSpaces{Bucket: "bucket", Key: "key", Lineage: "lineage", Serial: 8, VersionID: "version-2", RawSHA256: strings.Repeat("b", 64)}}
	intent, err := BuildProtectedApplyTerminalIntent(prestate, rule, poststate, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", strings.Repeat("b", 40), strings.Repeat("c", 64))
	if err != nil || !intent.Valid() {
		t.Fatalf("valid terminal intent rejected: %+v %v", intent, err)
	}
	intent.ApprovedTransitionRule.StateChange = "no-op"
	if intent.Valid() {
		t.Fatal("terminal intent accepted a transition rule mutation")
	}
}
