package database

import "testing"

func TestTargetIdentitySHA256FencesEveryAcceptedTargetField(t *testing.T) {
	target := TargetIdentity{ServiceID: "app-pg", ServiceGeneration: 4, BindingID: "primary",
		BindingGeneration: 2, Engine: EnginePostgreSQL, Database: "app", Role: "app_writer"}
	baseline, err := TargetIdentitySHA256(target)
	if err != nil || len(baseline) != 64 {
		t.Fatalf("accepted target digest = %q, %v", baseline, err)
	}
	for name, mutate := range map[string]func(*TargetIdentity){
		"service":            func(v *TargetIdentity) { v.ServiceID = "other" },
		"service generation": func(v *TargetIdentity) { v.ServiceGeneration++ },
		"binding":            func(v *TargetIdentity) { v.BindingID = "secondary" },
		"binding generation": func(v *TargetIdentity) { v.BindingGeneration++ },
		"engine":             func(v *TargetIdentity) { v.Engine = EngineMySQL },
		"database":           func(v *TargetIdentity) { v.Database = "other" },
		"role":               func(v *TargetIdentity) { v.Role = "other_writer" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := target
			mutate(&changed)
			digest, err := TargetIdentitySHA256(changed)
			if err != nil || digest == baseline {
				t.Fatalf("changed target reused digest: %q, %v", digest, err)
			}
		})
	}
	invalid := target
	invalid.BindingGeneration = 0
	if _, err := TargetIdentitySHA256(invalid); err == nil {
		t.Fatal("incomplete target identity was accepted")
	}
}
