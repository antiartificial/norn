package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

func TestMigrationPostconditionDigestUsesReviewedSpecAndAcceptedEngine(t *testing.T) {
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, Migrations: "migrate",
		Databases:              []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"migration"}}},
		MigrationPostcondition: &model.MigrationPostconditionSpec{Query: "SELECT version FROM migrations", ExpectedValue: "42"}}
	target := database.TargetIdentity{ServiceID: "service", ServiceGeneration: 1, BindingID: "binding",
		BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "app", Role: "writer"}
	check, digest, err := migrationPostconditionForSpec(spec, target)
	if err != nil || check.Engine != database.EnginePostgreSQL || digest == "" {
		t.Fatalf("reviewed postcondition=%+v digest=%q err=%v", check, digest, err)
	}
	changed := target
	changed.Engine = database.EngineMySQL
	_, otherDigest, err := migrationPostconditionForSpec(spec, changed)
	if err != nil || otherDigest == digest {
		t.Fatal("different accepted engine reused the reviewed postcondition digest")
	}
	spec.MigrationPostcondition = nil
	if _, _, err := migrationPostconditionForSpec(spec, target); err == nil {
		t.Fatal("migration without reviewed postcondition was admitted")
	}
}

func TestMigrationIntentUsesAcceptedTargetIdentityAndNoConnectionMaterial(t *testing.T) {
	target := database.TargetIdentity{ServiceID: "app-pg", ServiceGeneration: 3,
		BindingID: "app-primary", BindingGeneration: 7, Engine: database.EnginePostgreSQL,
		Database: "app", Role: "app_writer"}
	command := "./migrate --database $DATABASE_URL"
	intent, err := migrationIntentForAcceptedTarget(target, strings.Repeat("a", 64), command, strings.Repeat("b", 64), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	commandHash := sha256.Sum256([]byte(command))
	targetHash, _ := database.TargetIdentitySHA256(target)
	if intent.CommandSHA256 != hex.EncodeToString(commandHash[:]) || intent.TargetSHA256 != targetHash ||
		intent.TargetBindingID != target.BindingID || intent.TargetGeneration != int64(target.BindingGeneration) {
		t.Fatalf("migration intent does not bind reviewed command and target: %+v", intent)
	}
	encoded, _ := json.Marshal(intent)
	if strings.Contains(string(encoded), command) || strings.Contains(string(encoded), "DATABASE_URL") {
		t.Fatal("migration intent persisted command or connection material")
	}
	changed := target
	changed.ServiceGeneration++
	other, err := migrationIntentForAcceptedTarget(changed, strings.Repeat("a", 64), command, strings.Repeat("b", 64), time.Minute)
	if err != nil || other.TargetSHA256 == intent.TargetSHA256 {
		t.Fatalf("repointed service reused migration target digest: %+v, %v", other, err)
	}
}
