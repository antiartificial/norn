package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"norn/v2/api/database"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/model"
)

// migrationPostconditionForSpec binds the reviewed source assertion to the
// accepted engine before a migration intent is reserved. A replacement claim
// must recover this same pinned source or leave the original effect pending.
func migrationPostconditionForSpec(spec *model.InfraSpec, target database.TargetIdentity) (database.MigrationPostconditionSQL, string, error) {
	if spec == nil || spec.MigrationPostcondition == nil || spec.Migrations == "" ||
		!spec.NamedDatabases() || spec.EffectiveMigrationDatabase() == "" {
		return database.MigrationPostconditionSQL{}, "", fmt.Errorf("migration has no reviewed postcondition on a named database")
	}
	check := database.MigrationPostconditionSQL{Engine: target.Engine,
		Query: spec.MigrationPostcondition.Query, ExpectedValue: spec.MigrationPostcondition.ExpectedValue}
	digest, err := check.SHA256()
	if err != nil {
		return database.MigrationPostconditionSQL{}, "", err
	}
	return check, digest, nil
}

// migrationIntentForAcceptedTarget builds the public, durable half of a
// migration launch from the resolver's recorded target. The private command
// environment is deliberately absent. Source and postcondition digests come
// from reviewed operation inputs and must be checked again at execution.
func migrationIntentForAcceptedTarget(target database.TargetIdentity, sourceSHA256, command, postconditionSHA256 string, timeout time.Duration) (supervisor.MigrationIntent, error) {
	targetSHA256, err := database.TargetIdentitySHA256(target)
	if err != nil {
		return supervisor.MigrationIntent{}, err
	}
	if strings.TrimSpace(command) == "" || len(command) > 64<<10 || target.BindingGeneration > uint64(^uint64(0)>>1) {
		return supervisor.MigrationIntent{}, fmt.Errorf("migration command or target generation is invalid")
	}
	commandDigest := sha256.Sum256([]byte(command))
	intent := supervisor.MigrationIntent{
		SourceSHA256: sourceSHA256, CommandSHA256: hex.EncodeToString(commandDigest[:]),
		TargetSHA256: targetSHA256, TargetBindingID: target.BindingID,
		TargetGeneration: int64(target.BindingGeneration), PostconditionSHA256: postconditionSHA256,
		TimeoutMillis: timeout.Milliseconds(),
	}
	return intent, nil
}
