package startup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		values    map[string]string
		want      Config
		wantError string
	}{
		{name: "compatibility defaults", values: nil, want: Config{SchemaMode: SchemaModeAuto, StartupMode: ModeActive, SchemaTimeout: 5 * time.Minute}},
		{name: "active schema check", values: map[string]string{SchemaModeEnv: " CHECK ", StartupModeEnv: " ACTIVE "}, want: Config{SchemaMode: SchemaModeCheck, StartupMode: ModeActive, SchemaTimeout: 5 * time.Minute}},
		{name: "migration command", values: map[string]string{SchemaModeEnv: "migrate-only", SchemaTimeoutEnv: "45s"}, want: Config{SchemaMode: SchemaModeMigrateOnly, StartupMode: ModeActive, SchemaTimeout: 45 * time.Second}},
		{name: "passive check", values: map[string]string{SchemaModeEnv: "check", StartupModeEnv: "passive"}, want: Config{SchemaMode: SchemaModeCheck, StartupMode: ModePassive, SchemaTimeout: 5 * time.Minute}},
		{name: "invalid schema mode", values: map[string]string{SchemaModeEnv: "apply"}, wantError: SchemaModeEnv},
		{name: "invalid startup mode", values: map[string]string{StartupModeEnv: "candidate"}, wantError: StartupModeEnv},
		{name: "passive cannot migrate", values: map[string]string{SchemaModeEnv: "migrate-only", StartupModeEnv: "passive"}, wantError: "requires"},
		{name: "passive cannot auto apply", values: map[string]string{StartupModeEnv: "passive"}, wantError: "requires"},
		{name: "invalid timeout", values: map[string]string{SchemaTimeoutEnv: "forever"}, wantError: SchemaTimeoutEnv},
		{name: "zero timeout", values: map[string]string{SchemaTimeoutEnv: "0s"}, wantError: SchemaTimeoutEnv},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(env(tt.values))
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("config = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestWriteContractProbe(t *testing.T) {
	var out bytes.Buffer
	handled, err := WriteContractProbe([]string{ContractProbeArgument}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("contract probe was not handled")
	}
	var contract Contract
	if err := json.Unmarshal(out.Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Name != ContractName || len(contract.PassiveRoutes) != 3 || contract.SchemaContract != (SchemaContract{
		ReaderVersion: 5, WriterVersion: 31, CatalogMigrationVersion: 40,
		CatalogMinimumReaderVersion: 5, CatalogMinimumWriterVersion: 31,
	}) {
		t.Fatalf("unexpected contract: %#v", contract)
	}

	out.Reset()
	handled, err = WriteContractProbe([]string{"serve"}, &out)
	if err != nil || handled || out.Len() != 0 {
		t.Fatalf("ordinary invocation handled=%v err=%v output=%q", handled, err, out.String())
	}
}
