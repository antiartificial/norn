package controlrecovery

import "testing"

func TestInspectionRegistryPinsCurrentSchemaAndSensitiveExclusions(t *testing.T) {
	registry := InspectionRegistry()
	if len(registry) != 38 {
		t.Fatalf("registry has %d tables, want 38", len(registry))
	}
	if err := validateRegistry(registry); err != nil {
		t.Fatalf("registry validation: %v", err)
	}

	wantExcluded := map[string][]string{
		"operations":                   {"payload", "metadata", "message", "last_error"},
		"operation_request_identities": {"request_key"},
		"operation_acceptance_intents": {"request_canonical_bytes", "canonical_bytes", "signature"},
		"fleet_github_dispatches":      {"dispatch_nonce", "workflow_url"},
		"notification_channels":        {"url", "token", "user_key"},
		"access_enrollments":           {"code_hash", "verifier_hash", "source_hash"},
		"step_up_challenges":           {"token_jti", "resource", "nonce_hash"},
		"exec_sessions":                {"command", "owner_token"},
		"webhook_deliveries":           {"payload", "metadata"},
		"recovery_drills":              {"evidence"},
		"operation_checkpoints":        {"outputs"},
		"database_catalog_revisions":   {"catalog"},
		"operation_effects":            {"claim_owner", "claim_generation", "input_digest", "launch_payload", "supervisor_execution_id", "runtime_instance_id", "result_digest", "result_reference", "evidence_source", "evidence_reference", "evidence_observed_at"},
		"restart_effect_sources":       {},
	}
	byTable := make(map[string]Table, len(registry))
	for _, table := range registry {
		byTable[table.Name] = table
	}
	for tableName, names := range wantExcluded {
		table, exists := byTable[tableName]
		if !exists {
			t.Fatalf("missing table %s", tableName)
		}
		columns := make(map[string]bool, len(table.Columns))
		for _, column := range table.Columns {
			columns[column.Name] = column.Include
		}
		for _, name := range names {
			included, classified := columns[name]
			if !classified {
				t.Errorf("%s.%s is not classified", tableName, name)
			} else if included {
				t.Errorf("%s.%s is included in inspection output", tableName, name)
			}
		}
	}
}

func TestInspectionRegistryReturnsIndependentCopies(t *testing.T) {
	first := InspectionRegistry()
	first[0].Name = "changed"
	first[1].Columns[0].Name = "changed"
	first[2].OrderBy[0] = "changed"
	second := InspectionRegistry()
	if second[0].Name == "changed" || second[1].Columns[0].Name == "changed" || second[2].OrderBy[0] == "changed" {
		t.Fatal("registry mutation escaped returned copy")
	}
}

func TestSchemaClassificationErrorDoesNotPrintIdentifiers(t *testing.T) {
	err := (&SchemaClassificationError{UnknownTables: 1, MissingColumns: 2}).Error()
	if err != "control inspection refused unclassified schema (unknown tables=1, missing tables=0, unknown columns=0, missing columns=2, primary key mismatches=0)" {
		t.Fatalf("unexpected safe error: %q", err)
	}
}
