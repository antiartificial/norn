package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// MySQLSourceReconciliationLink is signed metadata on a normal source-kind
// operation. Its payload remains the exact original source request, allowing
// source staging and retention to use their existing receipt contract after
// a separately proved transfer.
type MySQLSourceReconciliationLink struct {
	PriorSourceOperationID string `json:"priorSourceOperationId"`
	PriorSourceDigest      string `json:"priorSourceDigest"`
	Checkpoint             string `json:"checkpoint"`
	RuntimeFenceEpoch      string `json:"runtimeFenceEpoch"`
	RuntimeFenceOwner      string `json:"runtimeFenceOwner"`
}

type MySQLSourceReconciliationAcceptanceInput struct {
	PriorSourceOperationID string
	Actor                  OperationActor
	Key                    string
	Audit                  AcceptanceAuditContext
}

// AcceptPrivateMySQLSourceReconciliation signs a named failed predecessor.
// Admission reads control state only; it does not observe or mutate Nomad or
// MySQL. The catalog-gated guard rejects competing successor request keys.
func (db *DB) AcceptPrivateMySQLSourceReconciliation(ctx context.Context, acceptance *PGOperationStore, input MySQLSourceReconciliationAcceptanceInput) (AcceptedOperation, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db ||
		strings.TrimSpace(input.PriorSourceOperationID) == "" || strings.TrimSpace(input.Key) == "" ||
		strings.TrimSpace(input.Actor.Issuer) == "" || strings.TrimSpace(input.Actor.Subject) == "" {
		return AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	authority, err := acceptance.Authority(ctx)
	if err != nil {
		return AcceptedOperation{}, err
	}
	identity := OperationRequestIdentity{Authority: authority, Actor: input.Actor,
		Kind:     MySQLSourceSnapshotOperationKind,
		Resource: "mysql-source/reconcile/" + input.PriorSourceOperationID, Key: input.Key}
	if existing, err := acceptance.ResolveIdentity(ctx, identity); err == nil {
		var link MySQLSourceReconciliationLink
		_, requestErr := sourceSnapshotRequestFromAccepted(existing)
		if existing.Operation.Kind != MySQLSourceSnapshotOperationKind || existing.Operation.MaxAttempts != 1 ||
			existing.Operation.Source != "private-mysql-source-reconciliation" ||
			requestErr != nil ||
			decodeMySQLSourceReconciliationLink(existing.Operation.Metadata, &link) != nil ||
			link.PriorSourceOperationID != input.PriorSourceOperationID {
			return AcceptedOperation{}, &AcceptanceConflictError{Identity: identity}
		}
		return existing, nil
	} else if !errors.Is(err, ErrAcceptanceNotFound) {
		return AcceptedOperation{}, err
	}
	prior, err := acceptance.VerifyAcceptedOperation(ctx, input.PriorSourceOperationID)
	if err != nil || prior.Operation.Kind != MySQLSourceSnapshotOperationKind || prior.Operation.Status != model.OperationFailed ||
		prior.Operation.MaxAttempts != 1 || prior.Operation.Metadata["manualRecoveryRequired"] != true {
		return AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	source, err := sourceSnapshotRequestFromAccepted(prior)
	if err != nil {
		return AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	inspection, err := db.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, input.PriorSourceOperationID)
	if err != nil || !mysqlSourceReconciliationCheckpoint(inspection.IntentState) || !inspection.RuntimeFenceHeld {
		return AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	var epoch int64
	var owner string
	if err := db.Pool.QueryRow(ctx, `SELECT runtime_fence_epoch,runtime_fence_owner FROM mysql_source_snapshot_intents WHERE operation_id=$1`,
		input.PriorSourceOperationID).Scan(&epoch, &owner); err != nil || epoch <= 0 || owner != "mysql-source-snapshot:"+input.PriorSourceOperationID {
		return AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	link := MySQLSourceReconciliationLink{PriorSourceOperationID: input.PriorSourceOperationID,
		PriorSourceDigest: prior.Intent.CanonicalDigest, Checkpoint: inspection.IntentState,
		RuntimeFenceEpoch: strconv.FormatInt(epoch, 10), RuntimeFenceOwner: owner}
	encoded, err := json.Marshal(source)
	if err != nil {
		return AcceptedOperation{}, err
	}
	var payload map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return AcceptedOperation{}, err
	}
	encodedMetadata, err := json.Marshal(link)
	if err != nil {
		return AcceptedOperation{}, err
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal(encodedMetadata, &metadata); err != nil {
		return AcceptedOperation{}, err
	}
	entry := OperationAcceptance{Identity: identity, Audit: input.Audit,
		Operation: model.Operation{ID: uuid.NewString(), Kind: MySQLSourceSnapshotOperationKind,
			App: source.JobIdentity.App, Ref: source.JobIdentity.DeploymentID, Status: model.OperationQueued,
			Risk: "high", Source: "private-mysql-source-reconciliation", MaxAttempts: 1, Payload: payload, Metadata: metadata}}
	entry.Fingerprint, err = CanonicalOperationRequestFingerprint(entry)
	if err != nil {
		return AcceptedOperation{}, err
	}
	return acceptance.acceptWithGuard(ctx, entry, nil, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockMySQLCatalogGate(ctx, tx); err != nil {
			return err
		}
		active, err := loadActiveDatabaseCatalog(ctx, tx)
		if err != nil || active.Revision != source.CatalogRevision {
			return ErrMySQLSourceSnapshotFence
		}
		resolver, err := database.NewResolver(active.Catalog)
		if err != nil {
			return ErrMySQLSourceSnapshotFence
		}
		resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: source.ProfileID,
			Purpose: database.PurposeApplication, LogicalResourceID: source.LogicalID, Expected: &source.Source})
		if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != source.Maintenance {
			return ErrMySQLSourceSnapshotFence
		}
		var status, checkpoint, intentID, owner, liveOwner, profileID, logicalID, dumpDigest string
		var manual, fenceActive, stopIntended, stopProved, lockIntended, lockProved, stageIntended, stageReceipt, publishIntended, retainedDescriptor, retentionReceipt bool
		var epoch, liveEpoch, revision int64
		var savedSource, savedMaintenance, savedJob []byte
		wantSource, _ := json.Marshal(source.Source)
		wantMaintenance, _ := json.Marshal(source.Maintenance)
		wantJob, _ := json.Marshal(source.JobIdentity)
		err = tx.QueryRow(ctx, `SELECT p.status,COALESCE((p.metadata->>'manualRecoveryRequired')::boolean,false),
			i.state,i.acceptance_intent_id,i.catalog_revision,i.profile_id,i.logical_id,
			i.source,i.maintenance,i.job_identity,i.dump_tool_sha256,
			i.stop_intended_at IS NOT NULL,i.stop_proved_at IS NOT NULL,i.lock_intended_at IS NOT NULL,
			i.lock_proved_at IS NOT NULL,i.stage_intended_at IS NOT NULL,
			i.artifact_receipt_canonical IS NOT NULL,i.artifact_publish_intended_at IS NOT NULL,
			i.retained_artifact IS NOT NULL,i.retention_receipt_canonical IS NOT NULL,
			i.runtime_fence_epoch,i.runtime_fence_owner,f.active,f.epoch,f.owner
			FROM mysql_source_snapshot_intents i JOIN operations p ON p.id=i.operation_id
			CROSS JOIN runtime_mutation_fence f WHERE i.operation_id=$1 AND f.singleton=true
			AND i.source_key=$2 FOR UPDATE OF p,i,f`, input.PriorSourceOperationID,
			mysqlRuntimePhysicalKeyForCatalog(active.Catalog, source.Source)).Scan(&status, &manual, &checkpoint, &intentID,
			&revision, &profileID, &logicalID, &savedSource, &savedMaintenance, &savedJob, &dumpDigest,
			&stopIntended, &stopProved, &lockIntended, &lockProved, &stageIntended, &stageReceipt,
			&publishIntended, &retainedDescriptor, &retentionReceipt,
			&epoch, &owner, &fenceActive, &liveEpoch, &liveOwner)
		if err != nil || status != "failed" || !manual || checkpoint != link.Checkpoint ||
			!stopIntended || (mysqlSourceLockCheckpoint(checkpoint) && (!stopProved || !lockIntended)) ||
			(checkpoint == "stop-proved" && !stopProved) ||
			(checkpoint == "lock-proved" && !lockProved) ||
			(checkpoint == "stage-intended" && (!lockProved || !stageIntended || stageReceipt)) ||
			(checkpoint == "publish-intended" && (!inspection.StageReceiptVerified || !lockProved || !stageIntended ||
				!stageReceipt || !publishIntended || !retainedDescriptor || retentionReceipt)) ||
			intentID != prior.AcceptanceIntentID || revision != source.CatalogRevision || profileID != source.ProfileID ||
			logicalID != source.LogicalID || !sameJSON(savedSource, wantSource) ||
			!sameJSON(savedMaintenance, wantMaintenance) || !sameJSON(savedJob, wantJob) ||
			dumpDigest != source.DumpToolSHA256 || !fenceActive || strconv.FormatInt(epoch, 10) != link.RuntimeFenceEpoch ||
			owner != link.RuntimeFenceOwner || liveEpoch != epoch || liveOwner != owner {
			return ErrMySQLSourceSnapshotFence
		}
		var competing bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operations WHERE kind=$1 AND source='private-mysql-source-reconciliation'
			AND metadata->>'priorSourceOperationId'=$2 AND status<>'failed')`, MySQLSourceSnapshotOperationKind,
			input.PriorSourceOperationID).Scan(&competing); err != nil || competing {
			return ErrMySQLSourceSnapshotFence
		}
		return nil
	})
}

func decodeMySQLSourceReconciliationLink(metadata map[string]interface{}, link *MySQLSourceReconciliationLink) error {
	keys := []string{"priorSourceOperationId", "priorSourceDigest", "checkpoint", "runtimeFenceEpoch", "runtimeFenceOwner"}
	signed := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		value, ok := metadata[key].(string)
		if !ok || value == "" {
			return ErrMySQLSourceSnapshotFence
		}
		signed[key] = value
	}
	encoded, err := json.Marshal(signed)
	if err != nil {
		return err
	}
	if err := decodeStrictAcceptanceJSON(encoded, link); err != nil {
		return err
	}
	epoch, err := strconv.ParseInt(link.RuntimeFenceEpoch, 10, 64)
	if link.PriorSourceOperationID == "" || link.PriorSourceDigest == "" ||
		!mysqlSourceReconciliationCheckpoint(link.Checkpoint) ||
		err != nil || epoch <= 0 || strconv.FormatInt(epoch, 10) != link.RuntimeFenceEpoch ||
		link.RuntimeFenceOwner != "mysql-source-snapshot:"+link.PriorSourceOperationID {
		return ErrMySQLSourceSnapshotFence
	}
	return nil
}

func mysqlSourceReconciliationCheckpoint(checkpoint string) bool {
	return checkpoint == "stop-intended" || checkpoint == "lock-intended" ||
		checkpoint == "stop-proved" || checkpoint == "lock-proved" || checkpoint == "stage-intended" || checkpoint == "publish-intended"
}

func mysqlSourceLockCheckpoint(checkpoint string) bool {
	return checkpoint == "lock-intended" || checkpoint == "lock-proved" || checkpoint == "stage-intended" || checkpoint == "publish-intended"
}
