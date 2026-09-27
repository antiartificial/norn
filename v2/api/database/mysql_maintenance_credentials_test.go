package database

import "testing"

func TestMySQLMaintenanceCredentialsAreStrictAndGenerationBound(t *testing.T) {
	base := testCatalog()
	mysql := &base.Bindings[4]
	mysql.MySQLMaintenance = &MySQLMaintenanceCredentials{
		Generation: 1, RuntimeAccountHost: "%", SnapshotAccountHost: "%", RestoreAccountHost: "%",
		SnapshotRole: "wordpress_snapshot", SnapshotCredentialRef: "secret:apps/wp-snapshot",
		RestoreRole: "wordpress_restore", RestoreCredentialRef: "secret:apps/wp-restore",
		FenceRole: "wordpress_fence", FenceCredentialRef: "secret:apps/wp-fence", FenceAccountHost: "%",
	}
	if err := ValidateCatalog(base); err != nil {
		t.Fatalf("valid MySQL maintenance binding rejected: %v", err)
	}
	resolver, err := NewResolver(base)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "wordpress-db"})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != *mysql.MySQLMaintenance {
		t.Fatalf("maintenance identity was not preserved in resolved binding: %#v, %v", resolved.MySQLMaintenance, err)
	}
	mysql.MySQLMaintenance.RestoreRole = "mutated"
	if resolved.MySQLMaintenance.RestoreRole != "wordpress_restore" {
		t.Fatal("resolved maintenance identity aliases mutable catalog input")
	}

	for name, mutate := range map[string]func(*MySQLMaintenanceCredentials){
		"zero generation":          func(m *MySQLMaintenanceCredentials) { m.Generation = 0 },
		"runtime role":             func(m *MySQLMaintenanceCredentials) { m.RestoreRole = "wp" },
		"snapshot runtime role":    func(m *MySQLMaintenanceCredentials) { m.SnapshotRole = "wp" },
		"incomplete snapshot":      func(m *MySQLMaintenanceCredentials) { m.SnapshotCredentialRef = "" },
		"same snapshot role":       func(m *MySQLMaintenanceCredentials) { m.SnapshotRole = m.RestoreRole },
		"same snapshot credential": func(m *MySQLMaintenanceCredentials) { m.SnapshotCredentialRef = m.RestoreCredentialRef },
		"same roles":               func(m *MySQLMaintenanceCredentials) { m.FenceRole = m.RestoreRole },
		"runtime credential":       func(m *MySQLMaintenanceCredentials) { m.RestoreCredentialRef = "secret:apps/wp" },
		"same credentials":         func(m *MySQLMaintenanceCredentials) { m.FenceCredentialRef = m.RestoreCredentialRef },
		"missing runtime host":     func(m *MySQLMaintenanceCredentials) { m.RuntimeAccountHost = "" },
		"missing restore host":     func(m *MySQLMaintenanceCredentials) { m.RestoreAccountHost = "" },
		"bad fence host":           func(m *MySQLMaintenanceCredentials) { m.FenceAccountHost = "bad\nhost" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneCatalog(base)
			candidate.Bindings[4].MySQLMaintenance.RestoreRole = "wordpress_restore"
			mutate(candidate.Bindings[4].MySQLMaintenance)
			if err := ValidateCatalog(candidate); err == nil {
				t.Fatal("unsafe maintenance identity accepted")
			}
		})
	}

	rotated := cloneCatalog(base)
	rotated.Bindings[4].MySQLMaintenance.SnapshotRole = "wordpress_snapshot_v2"
	if err := ValidateTransition(base, rotated); err == nil {
		t.Fatal("maintenance identity changed without a binding generation bump")
	}
	rotated.Bindings[4].Generation++
	if err := ValidateTransition(base, rotated); err != nil {
		t.Fatalf("maintenance identity change with binding generation bump rejected: %v", err)
	}
}

func TestMySQLSnapshotUsesOnlyMaintenanceCredential(t *testing.T) {
	resolved := ResolvedBinding{
		Target:           TargetIdentity{Engine: EngineMySQL, BindingID: "wordpress", Role: "wp", Database: "wordpress"},
		CredentialRef:    "secret:apps/wp",
		MySQLMaintenance: &MySQLMaintenanceCredentials{Generation: 3, RuntimeAccountHost: "%", SnapshotRole: "wp_snapshot", SnapshotAccountHost: "%", SnapshotCredentialRef: "secret:apps/wp-snapshot", RestoreRole: "wp_restore", RestoreAccountHost: "%", RestoreCredentialRef: "secret:apps/wp-restore", FenceRole: "wp_fence", FenceCredentialRef: "secret:apps/wp-fence", FenceAccountHost: "%"},
	}
	snapshot, err := MySQLSnapshotBinding(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Target.Role != "wp_snapshot" || snapshot.CredentialRef != "secret:apps/wp-snapshot" || resolved.Target.Role != "wp" || resolved.CredentialRef != "secret:apps/wp" {
		t.Fatalf("snapshot identity did not isolate runtime credential: %#v", snapshot)
	}
	resolved.MySQLMaintenance = nil
	if _, err := MySQLSnapshotBinding(resolved); err == nil {
		t.Fatal("snapshot without a separate maintenance credential was accepted")
	}
}

func TestMySQLRestoreUsesOnlyMaintenanceCredential(t *testing.T) {
	resolved := ResolvedBinding{
		Target:           TargetIdentity{Engine: EngineMySQL, BindingID: "wordpress", Role: "wp", Database: "wordpress"},
		CredentialRef:    "secret:apps/wp",
		MySQLMaintenance: &MySQLMaintenanceCredentials{Generation: 3, RuntimeAccountHost: "%", RestoreRole: "wp_restore", RestoreAccountHost: "%", RestoreCredentialRef: "secret:apps/wp-restore", FenceRole: "wp_fence", FenceCredentialRef: "secret:apps/wp-fence", FenceAccountHost: "%"},
	}
	restore, err := MySQLRestoreBinding(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if restore.Target.Role != "wp_restore" || restore.CredentialRef != "secret:apps/wp-restore" || resolved.Target.Role != "wp" || resolved.CredentialRef != "secret:apps/wp" {
		t.Fatalf("restore identity did not isolate runtime credential: %#v", restore)
	}
	resolved.MySQLMaintenance = nil
	if _, err := MySQLRestoreBinding(resolved); err == nil {
		t.Fatal("restore without a separate maintenance credential was accepted")
	}
}
