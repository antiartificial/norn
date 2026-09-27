package model

import (
	"path/filepath"
	"testing"
)

func TestMobilityFixtureSpecIsValidAndInert(t *testing.T) {
	spec, err := LoadInfraSpec(filepath.Join("..", "..", "infra", "mobility-fixture", "infraspec.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	result := ValidateSpec(spec)
	if !result.Valid {
		t.Fatalf("mobility fixture spec invalid: %+v", result.Findings)
	}
	if spec.Deploy || spec.App != "v3-mobility-fixture" || len(spec.Databases) != 1 || len(spec.Volumes) != 1 ||
		spec.Processes["web"].Port != 8080 || spec.Processes["worker"].Schedule == "" || spec.Processes["tick"].Schedule == "" {
		t.Fatalf("fixture does not require database, files, web, worker and schedule while deploy-disabled: %+v", spec)
	}
}
