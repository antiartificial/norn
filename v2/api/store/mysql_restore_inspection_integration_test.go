package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/database"
)

func TestExpiredExecutingMySQLRestoreFailsClosedWithVerifiableInspection(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	target := database.TargetIdentity{ServiceID: "mysql-target", ServiceGeneration: 7, BindingID: "mysql-binding", BindingGeneration: 9, Engine: database.EngineMySQL, Database: "app", Role: "app"}
	artifact := database.MySQLSQLArtifact{Format: database.MySQLSQLArtifactV2, Source: database.TargetIdentity{ServiceID: "mysql-source", ServiceGeneration: 3, BindingID: "source-binding", BindingGeneration: 4, Engine: database.EngineMySQL, Database: "source", Role: "reader"}, Bytes: 1234, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	request := MySQLRestoreRequest{CatalogRevision: 1, ProfileID: "private-profile-selector", LogicalID: "private-logical-selector", Target: target, Artifact: artifact, ArtifactPath: "/private/stage/restore.sql"}
	input := newAcceptance(t, stores[0], "mysql-inspection-"+uuid.NewString(), "operator", "mysql/app", false)
	input.Identity.Kind, input.Identity.Resource = MySQLRestoreOperationKind, "mysql/app"
	input.Operation.Kind, input.Operation.MaxAttempts = MySQLRestoreOperationKind, 1
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	input.Operation.Payload = nil
	if err := json.Unmarshal(encoded, &input.Operation.Payload); err != nil {
		t.Fatal(err)
	}
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := stores[0].Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `INSERT INTO database_catalog_revisions
		(revision,catalog,catalog_digest,activated_by) VALUES (1,'{}','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','test')`); err != nil {
		t.Fatal(err)
	}
	targetJSON, _ := json.Marshal(target)
	artifactJSON, _ := json.Marshal(artifact)
	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operations SET status='running',attempts=1,locked_by='lost-worker',lock_generation=1,locked_until=now()-interval '1 second' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].Pool.Exec(ctx, `INSERT INTO mysql_restore_intents
		(operation_id,acceptance_intent_id,catalog_revision,profile_id,logical_id,target_key,target,artifact,artifact_path,state,started_at)
		VALUES ($1,$2,1,'private-profile-selector','private-logical-selector',$3,$4,$5,'/private/stage/restore.sql','executing',now())`,
		accepted.Operation.ID, accepted.AcceptanceIntentID, uuid.NewString(), targetJSON, artifactJSON); err != nil {
		t.Fatal(err)
	}

	if err := dbs[0].RecoverExpiredOperations(ctx); err != nil {
		t.Fatal(err)
	}
	inspection, err := stores[0].InspectMySQLRestore(ctx, accepted.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.IntentState != "needs-inspection" || inspection.OperationStatus != "failed" || !inspection.ManualRecovery {
		t.Fatalf("unsafe recovered state: %+v", inspection)
	}
	if inspection.AcceptanceIntentID != accepted.AcceptanceIntentID || inspection.CanonicalDigest == "" || inspection.Signature.Value == "" || inspection.CatalogDigest == "" || inspection.Target != target || inspection.Artifact != artifact {
		t.Fatalf("inspection omitted signed identity: %+v", inspection)
	}
	serialized, _ := json.Marshal(inspection)
	if string(serialized) == "" || containsAny(string(serialized), request.ArtifactPath, request.ProfileID, request.LogicalID) {
		t.Fatalf("inspection disclosed private routing material: %s", serialized)
	}
	var attempts int
	if err := dbs[0].Pool.QueryRow(ctx, `SELECT attempts FROM operations WHERE id=$1`, accepted.Operation.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("restore was retried: attempts=%d err=%v", attempts, err)
	}

	if _, err := dbs[0].Pool.Exec(ctx, `UPDATE operation_acceptance_intents SET signature='forged' WHERE id=$1`, accepted.AcceptanceIntentID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].InspectMySQLRestore(ctx, accepted.Operation.ID); !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("tampered signed identity was inspectable: %v", err)
	}
}

func containsAny(value string, forbidden ...string) bool {
	for _, item := range forbidden {
		if item != "" && strings.Contains(value, item) {
			return true
		}
	}
	return false
}
