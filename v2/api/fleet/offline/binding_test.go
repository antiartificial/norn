package offline

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func goodBinding() Binding {
	start := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	return Binding{
		SchemaVersion: BindingSchema, PlanID: "11111111-1111-4111-8111-111111111111", TargetID: "target-1",
		Resource: WorkResource, SourceCommit: strings.Repeat("a", 40),
		SavedPlanSHA256: strings.Repeat("b", 64), EffectSHA256: strings.Repeat("c", 64),
		Backend: Backend{Bucket: "work-state", Key: "staging/terraform.tfstate", VersionID: "version-1",
			Lineage: "lineage-1", Serial: 22, RawSHA256: strings.Repeat("d", 64)},
		ProviderAccountUUID: "work-account", NodeName: WorkNode, DropletID: WorkDropletID,
		OwnerAuthorizationID: strings.Repeat("9", 64), OwnerEnvelopeSHA256: strings.Repeat("e", 64), RunnerPrincipalSubject: "offline-runner-1",
		DispatchNonceSHA256: strings.Repeat("f", 64), FenceGeneration: 29,
		CreatedAt: start, ExpiresAt: start.Add(10 * time.Minute),
	}
}

func claimOf(b Binding) AttemptClaim {
	return AttemptClaim{PlanID: b.PlanID, TargetID: b.TargetID, Resource: b.Resource, SourceCommit: b.SourceCommit,
		SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256,
		CandidateSHA256: b.CandidateSHA256, ProviderNodes: b.ProviderNodes,
		Backend: b.Backend, ProviderAccountUUID: b.ProviderAccountUUID,
		NodeName: b.NodeName, DropletID: b.DropletID, OwnerAuthorizationID: b.OwnerAuthorizationID,
		OwnerEnvelopeSHA256:    b.OwnerEnvelopeSHA256,
		RunnerPrincipalSubject: b.RunnerPrincipalSubject, DispatchNonceSHA256: b.DispatchNonceSHA256,
		FenceGeneration: b.FenceGeneration}
}

func TestBindingFailsClosedForMissingOrChangedIdentity(t *testing.T) {
	b := goodBinding()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Binding){
		"source":   func(v *Binding) { v.SourceCommit = strings.Repeat("z", 40) },
		"plan":     func(v *Binding) { v.SavedPlanSHA256 = "" },
		"effect":   func(v *Binding) { v.EffectSHA256 = "" },
		"backend":  func(v *Binding) { v.Backend.RawSHA256 = "" },
		"serial":   func(v *Binding) { v.Backend.Serial = 0 },
		"account":  func(v *Binding) { v.ProviderAccountUUID = "" },
		"node":     func(v *Binding) { v.NodeName = "control-01" },
		"droplet":  func(v *Binding) { v.DropletID = "610327216" },
		"owner":    func(v *Binding) { v.OwnerEnvelopeSHA256 = "" },
		"owner id": func(v *Binding) { v.OwnerAuthorizationID = "" },
		"runner":   func(v *Binding) { v.RunnerPrincipalSubject = "" },
		"nonce":    func(v *Binding) { v.DispatchNonceSHA256 = "" },
		"fence":    func(v *Binding) { v.FenceGeneration = 0 },
		"lifetime": func(v *Binding) { v.ExpiresAt = v.CreatedAt.Add(16 * time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := b
			change(&changed)
			if !errors.Is(changed.Validate(), ErrInvalidBinding) {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestDecodeRejectsDuplicateAndUnknownFields(t *testing.T) {
	b := goodBinding()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBinding(raw)
	if err != nil || decoded.PlanID != b.PlanID {
		t.Fatalf("valid decode: %+v, %v", decoded, err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"planId":"11111111-1111-4111-8111-111111111111"`, `"planId":"11111111-1111-4111-8111-111111111111","planId":"22222222-2222-4222-8222-222222222222"`, 1),
		strings.Replace(string(raw), `"serial":22`, `"serial":22,"serial":23`, 1),
		strings.Replace(string(raw), `"planId":"11111111-1111-4111-8111-111111111111"`, `"planId":"11111111-1111-4111-8111-111111111111","extra":true`, 1),
	} {
		if _, err := DecodeBinding([]byte(bad)); !errors.Is(err, ErrInvalidBinding) {
			t.Fatalf("accepted ambiguous binding: %v", err)
		}
	}
}

func TestAttemptInheritsEveryBindingAndExpires(t *testing.T) {
	b := goodBinding()
	a := claimOf(b)
	now := b.CreatedAt.Add(time.Minute)
	if err := b.MatchAttempt(a, now); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*AttemptClaim){
		"plan":            func(v *AttemptClaim) { v.PlanID = "plan-2" },
		"source":          func(v *AttemptClaim) { v.SourceCommit = strings.Repeat("1", 40) },
		"saved plan":      func(v *AttemptClaim) { v.SavedPlanSHA256 = strings.Repeat("1", 64) },
		"effect":          func(v *AttemptClaim) { v.EffectSHA256 = strings.Repeat("1", 64) },
		"backend version": func(v *AttemptClaim) { v.Backend.VersionID = "version-2" },
		"backend bytes":   func(v *AttemptClaim) { v.Backend.RawSHA256 = strings.Repeat("1", 64) },
		"backend serial":  func(v *AttemptClaim) { v.Backend.Serial++ },
		"target":          func(v *AttemptClaim) { v.TargetID = "another-target" },
		"account":         func(v *AttemptClaim) { v.ProviderAccountUUID = "another-account" },
		"droplet":         func(v *AttemptClaim) { v.DropletID = "610327216" },
		"owner":           func(v *AttemptClaim) { v.OwnerEnvelopeSHA256 = strings.Repeat("1", 64) },
		"owner id":        func(v *AttemptClaim) { v.OwnerAuthorizationID = strings.Repeat("1", 64) },
		"runner":          func(v *AttemptClaim) { v.RunnerPrincipalSubject = "other" },
		"nonce":           func(v *AttemptClaim) { v.DispatchNonceSHA256 = strings.Repeat("1", 64) },
		"fence":           func(v *AttemptClaim) { v.FenceGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := a
			change(&changed)
			if !errors.Is(b.MatchAttempt(changed, now), ErrAttemptMismatch) {
				t.Fatal("changed claim accepted")
			}
		})
	}
	if !errors.Is(b.MatchAttempt(a, b.ExpiresAt), ErrAttemptMismatch) {
		t.Fatal("expired claim accepted")
	}
}
