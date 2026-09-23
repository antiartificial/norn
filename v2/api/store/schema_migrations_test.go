package store

import (
	"errors"
	"testing"
)

func TestValidateMigrationDefinitionsRejectsGapDuplicateAndOrder(t *testing.T) {
	valid := []SchemaMigration{
		{Version: 1, Name: "baseline", SQL: "SELECT 1", MinimumReaderVersion: 1, MinimumWriterVersion: 1},
		{Version: 2, Name: "add owner", SQL: "SELECT 2", MinimumReaderVersion: 1, MinimumWriterVersion: 1},
	}
	tests := []struct {
		name       string
		mutate     func([]SchemaMigration) []SchemaMigration
		wantReason string
	}{
		{
			name: "gap",
			mutate: func(items []SchemaMigration) []SchemaMigration {
				items[1].Version = 3
				return items
			},
			wantReason: "ordered and contiguous",
		},
		{
			name: "duplicate version",
			mutate: func(items []SchemaMigration) []SchemaMigration {
				items[1].Version = 1
				return items
			},
			wantReason: "ordered and contiguous",
		},
		{
			name: "out of order",
			mutate: func(items []SchemaMigration) []SchemaMigration {
				items[0], items[1] = items[1], items[0]
				return items
			},
			wantReason: "ordered and contiguous",
		},
		{
			name: "duplicate name",
			mutate: func(items []SchemaMigration) []SchemaMigration {
				items[1].Name = items[0].Name
				return items
			},
			wantReason: "duplicates version",
		},
		{
			name: "reader minimum regresses",
			mutate: func(items []SchemaMigration) []SchemaMigration {
				items[0].MinimumReaderVersion = 2
				return items
			},
			wantReason: "reader version cannot decrease",
		},
		{
			name: "writer minimum regresses",
			mutate: func(items []SchemaMigration) []SchemaMigration {
				items[0].MinimumWriterVersion = 2
				return items
			},
			wantReason: "writer version cannot decrease",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items := append([]SchemaMigration(nil), valid...)
			err := validateMigrationDefinitions(test.mutate(items))
			var definitionErr *MigrationDefinitionError
			if !errors.As(err, &definitionErr) {
				t.Fatalf("error = %T %v, want *MigrationDefinitionError", err, err)
			}
			if !contains(definitionErr.Reason, test.wantReason) {
				t.Fatalf("reason = %q, want substring %q", definitionErr.Reason, test.wantReason)
			}
		})
	}
	if err := validateMigrationDefinitions(valid); err != nil {
		t.Fatalf("valid definitions: %v", err)
	}
}

func TestMigrationChecksumCoversCompleteImmutableDefinition(t *testing.T) {
	base := SchemaMigration{
		Version: 1, Name: "baseline", SQL: "SELECT 1",
		MinimumReaderVersion: 1, MinimumWriterVersion: 1,
	}
	want := MigrationChecksum(base)
	if len(want) != 64 || !validChecksum(want) {
		t.Fatalf("checksum = %q, want lowercase SHA-256", want)
	}
	if got := MigrationChecksum(base); got != want {
		t.Fatalf("checksum is not deterministic: %q != %q", got, want)
	}
	mutations := []SchemaMigration{
		{Version: 2, Name: base.Name, SQL: base.SQL, MinimumReaderVersion: 1, MinimumWriterVersion: 1},
		{Version: 1, Name: "renamed", SQL: base.SQL, MinimumReaderVersion: 1, MinimumWriterVersion: 1},
		{Version: 1, Name: base.Name, SQL: "SELECT 2", MinimumReaderVersion: 1, MinimumWriterVersion: 1},
		{Version: 1, Name: base.Name, SQL: base.SQL, MinimumReaderVersion: 2, MinimumWriterVersion: 1},
		{Version: 1, Name: base.Name, SQL: base.SQL, MinimumReaderVersion: 1, MinimumWriterVersion: 2},
	}
	for _, mutation := range mutations {
		if got := MigrationChecksum(mutation); got == want {
			t.Fatalf("changed definition produced unchanged checksum: %+v", mutation)
		}
	}
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
