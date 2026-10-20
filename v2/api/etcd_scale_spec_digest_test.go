package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEtcdScaleSpecBindingRejectsSemanticDrift(t *testing.T) {
	cases := []struct {
		name     string
		accepted string
		drifted  string
	}{
		{name: "raised maximum", accepted: "    scaling:\n      min: 1\n      max: 2\n    health:\n      path: /ready\n", drifted: "    scaling:\n      min: 1\n      max: 3\n    health:\n      path: /ready\n"},
		{name: "added health check", accepted: "    scaling:\n      min: 1\n      max: 2\n", drifted: "    scaling:\n      min: 1\n      max: 2\n    health:\n      path: /ready\n"},
		{name: "removed fixed host port", accepted: "    hostPort: 18080\n    scaling:\n      min: 1\n      max: 1\n    health:\n      path: /ready\n", drifted: "    scaling:\n      min: 1\n      max: 2\n    health:\n      path: /ready\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apps := t.TempDir()
			dir := filepath.Join(apps, "widgets")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			prefix := "name: widgets\ndeploy: true\nregions:\n  us-central:\n    nomadRegion: global\n    datacenters: [dc1]\nprocesses:\n  web:\n    command: sleep 60\n"
			path := filepath.Join(dir, "infraspec.yaml")
			if err := os.WriteFile(path, []byte(prefix+tc.accepted), 0o600); err != nil {
				t.Fatal(err)
			}
			accepted, err := currentEtcdScaleSpecDigest(apps, "widgets")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := requireCurrentEtcdScaleSpecDigest(apps, "widgets", accepted); err != nil {
				t.Fatalf("exact accepted spec rejected: %v", err)
			}
			if err := os.WriteFile(path, []byte(prefix+tc.drifted), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := requireCurrentEtcdScaleSpecDigest(apps, "widgets", accepted); err == nil {
				t.Fatal("semantic InfraSpec drift retained accepted scale authority")
			}
		})
	}
}
