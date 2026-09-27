package database

import (
	"context"
	"strings"
	"testing"
)

func TestMySQLRestoreExpectationValidation(t *testing.T) {
	emptySHA := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	valid := MySQLRestoreExpectation{SchemaSHA256: emptySHA, DataSHA256: emptySHA, TableCount: 0}
	if !validMySQLRestoreExpectation(valid) {
		t.Fatal("valid empty-database expectation rejected")
	}
	for name, mutate := range map[string]func(*MySQLRestoreExpectation){
		"missing schema digest": func(value *MySQLRestoreExpectation) { value.SchemaSHA256 = "" },
		"uppercase data digest": func(value *MySQLRestoreExpectation) { value.DataSHA256 = strings.ToUpper(value.DataSHA256) },
		"negative table count":  func(value *MySQLRestoreExpectation) { value.TableCount = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if validMySQLRestoreExpectation(candidate) {
				t.Fatal("invalid expectation accepted")
			}
		})
	}
}

func TestMySQLRestoreV1ArtifactCannotReachExecutionOrVerification(t *testing.T) {
	artifact := MySQLSQLArtifact{
		Format: MySQLSQLArtifactV1,
		Source: TargetIdentity{ServiceID: "mysql", ServiceGeneration: 1, BindingID: "source", BindingGeneration: 1, Engine: EngineMySQL, Database: "source", Role: "source"},
		Bytes:  1, SHA256: strings.Repeat("0", 64),
	}
	if _, err := OpenVerifiedMySQLSQLArtifact("/does/not/matter", artifact); err == nil || !strings.Contains(err.Error(), "metadata is invalid") {
		t.Fatalf("legacy artifact did not fail closed: %v", err)
	}
	if err := VerifyMySQLRestoreTarget(context.Background(), ResolvedBinding{}, nil, artifact.Expectation); err == nil || !strings.Contains(err.Error(), "expectation is invalid") {
		t.Fatalf("missing expectation did not fail closed: %v", err)
	}
}

func TestNormalizeMySQLSchemaDefinitionRemovesTargetSpecificIdentity(t *testing.T) {
	definition := "CREATE DEFINER=`source_user`@`%` VIEW `v` AS select * from `source_db`.`items`"
	got := normalizeMySQLSchemaDefinition(definition, "source_db")
	want := "CREATE DEFINER=`<definer>` VIEW `v` AS select * from `<database>`.`items`"
	if got != want {
		t.Fatalf("normalized definition = %q, want %q", got, want)
	}
}
