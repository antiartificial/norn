package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

const MySQLSourceSnapshotOperationKind = "database.mysql-source-snapshot"

// MySQLSourceSnapshotJobIdentity is the exact WordPress job revision that a
// later private runner must stop and reobserve before locking source writes.
type MySQLSourceSnapshotJobIdentity struct {
	App                     string   `json:"app"`
	DeploymentID            string   `json:"deploymentId"`
	SpecDigest              string   `json:"specDigest"`
	Region                  string   `json:"region"`
	NomadRegion             string   `json:"nomadRegion"`
	JobID                   string   `json:"jobId"`
	JobVersion              string   `json:"jobVersion"`
	JobModifyIndex          string   `json:"jobModifyIndex"`
	AllocationIDs           []string `json:"allocationIds"`
	DatabaseBindingSchema   string   `json:"databaseBindingSchema"`
	DatabaseBindingSHA256   string   `json:"databaseBindingSha256"`
	DatabaseCatalogRevision string   `json:"databaseCatalogRevision"`
}

// MySQLSourceSnapshotRequest is signed before the artifact exists. The source
// and maintenance identities cannot be selected by the worker after acceptance.
type MySQLSourceSnapshotRequest struct {
	CatalogRevision int64                                `json:"catalogRevision"`
	ProfileID       string                               `json:"profileId"`
	LogicalID       string                               `json:"logicalId"`
	Source          database.TargetIdentity              `json:"source"`
	Maintenance     database.MySQLMaintenanceCredentials `json:"maintenance"`
	JobIdentity     MySQLSourceSnapshotJobIdentity       `json:"jobIdentity"`
	DumpToolSHA256  string                               `json:"dumpToolSha256"`
}

type MySQLSourceSnapshotIntent struct {
	OperationID        string
	AcceptanceIntentID string
	Request            MySQLSourceSnapshotRequest
	State              string
	Replayed           bool
}

var ErrMySQLSourceSnapshotFence = errors.New("MySQL source snapshot durable fence rejected the request")

// PrepareClaimedMySQLSourceSnapshot commits the source reservation under the
// same catalog lock used by runtime launches and restores. The permanent row
// deliberately precedes every external Nomad or MySQL effect; no automatic
// recovery releases it. This method does not claim that writes are quiesced.
func (db *DB) PrepareClaimedMySQLSourceSnapshot(ctx context.Context, acceptance *PGOperationStore, claim OperationClaim, request MySQLSourceSnapshotRequest) (MySQLSourceSnapshotIntent, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db || validateOperationClaim(claim) != nil || !validMySQLSourceSnapshotRequest(request) {
		return MySQLSourceSnapshotIntent{}, ErrMySQLSourceSnapshotFence
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, claim.OperationID())
	if err != nil {
		return MySQLSourceSnapshotIntent{}, err
	}
	if accepted.Operation.Kind != MySQLSourceSnapshotOperationKind || accepted.Operation.Status != model.OperationRunning || accepted.Operation.MaxAttempts != 1 || accepted.Operation.App != request.JobIdentity.App || !sameMySQLSourceSnapshotPayload(accepted.Operation.Payload, request) {
		return MySQLSourceSnapshotIntent{}, ErrMySQLSourceSnapshotFence
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MySQLSourceSnapshotIntent{}, err
	}
	defer tx.Rollback(context.Background())
	if err := lockMySQLCatalogGate(ctx, tx); err != nil {
		return MySQLSourceSnapshotIntent{}, err
	}
	var held bool
	if err := tx.QueryRow(ctx, `SELECT true FROM operations WHERE id=$1 AND kind=$2 AND status='running' AND locked_by=$3 AND lock_generation=$4 AND locked_until>clock_timestamp() FOR UPDATE`, claim.OperationID(), MySQLSourceSnapshotOperationKind, claim.OwnerID(), claim.Generation()).Scan(&held); err != nil || !held {
		return MySQLSourceSnapshotIntent{}, ownershipLost(claim)
	}
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil || active.Revision != request.CatalogRevision {
		return MySQLSourceSnapshotIntent{}, ErrDatabaseCatalogRevisionConflict
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLSourceSnapshotIntent{}, err
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Source})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
		return MySQLSourceSnapshotIntent{}, ErrMySQLSourceSnapshotFence
	}
	if err := rejectMySQLRuntimeLaunchReservations(ctx, tx, []database.TargetIdentity{request.Source}); err != nil {
		return MySQLSourceSnapshotIntent{}, ErrMySQLSourceSnapshotFence
	}
	if err := rejectMySQLRestoreMaintenanceFence(ctx, tx, []database.TargetIdentity{request.Source}, claim.OperationID()); err != nil {
		return MySQLSourceSnapshotIntent{}, ErrMySQLSourceSnapshotFence
	}
	source, _ := json.Marshal(request.Source)
	maintenance, _ := json.Marshal(request.Maintenance)
	job, _ := json.Marshal(request.JobIdentity)
	key := mysqlRuntimePhysicalKeyForCatalog(active.Catalog, request.Source)
	inserted, err := tx.Exec(ctx, `INSERT INTO mysql_source_snapshot_intents
		(operation_id,acceptance_intent_id,catalog_revision,profile_id,logical_id,source_key,source,maintenance,job_identity,dump_tool_sha256,state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'quiesce-intended') ON CONFLICT DO NOTHING`, claim.OperationID(), accepted.AcceptanceIntentID, request.CatalogRevision, request.ProfileID, request.LogicalID, key, source, maintenance, job, request.DumpToolSHA256)
	if err != nil {
		return MySQLSourceSnapshotIntent{}, err
	}
	var savedIntent, savedProfile, savedLogical, savedKey, savedDigest, state string
	var savedRevision int64
	var savedSource, savedMaintenance, savedJob []byte
	if err := tx.QueryRow(ctx, `SELECT acceptance_intent_id,catalog_revision,profile_id,logical_id,source_key,source,maintenance,job_identity,dump_tool_sha256,state FROM mysql_source_snapshot_intents WHERE operation_id=$1 FOR UPDATE`, claim.OperationID()).Scan(&savedIntent, &savedRevision, &savedProfile, &savedLogical, &savedKey, &savedSource, &savedMaintenance, &savedJob, &savedDigest, &state); err != nil || savedIntent != accepted.AcceptanceIntentID || savedRevision != request.CatalogRevision || savedProfile != request.ProfileID || savedLogical != request.LogicalID || savedKey != key || !sameJSON(savedSource, source) || !sameJSON(savedMaintenance, maintenance) || !sameJSON(savedJob, job) || savedDigest != request.DumpToolSHA256 || state != "quiesce-intended" {
		return MySQLSourceSnapshotIntent{}, ErrMySQLSourceSnapshotFence
	}
	if err := tx.Commit(ctx); err != nil {
		return MySQLSourceSnapshotIntent{}, err
	}
	return MySQLSourceSnapshotIntent{OperationID: claim.OperationID(), AcceptanceIntentID: accepted.AcceptanceIntentID, Request: request, State: state, Replayed: inserted.RowsAffected() == 0}, nil
}

func validMySQLSourceSnapshotRequest(request MySQLSourceSnapshotRequest) bool {
	job := request.JobIdentity
	if request.CatalogRevision <= 0 || strings.TrimSpace(request.ProfileID) == "" || strings.TrimSpace(request.LogicalID) == "" || request.Source.Engine != database.EngineMySQL || request.Source.ServiceGeneration == 0 || request.Source.BindingGeneration == 0 || request.Maintenance.Generation == 0 || request.Maintenance.SnapshotRole == "" || request.Maintenance.SnapshotAccountHost == "" || request.Maintenance.SnapshotCredentialRef == "" || job.App == "" || job.DeploymentID == "" || job.Region == "" || job.NomadRegion == "" || job.JobID != job.App || job.DatabaseBindingSchema == "" || job.DatabaseCatalogRevision != strconv.FormatInt(request.CatalogRevision, 10) || len(job.DatabaseBindingSHA256) != 64 || len(job.AllocationIDs) == 0 || len(request.DumpToolSHA256) != 64 || strings.ToLower(request.DumpToolSHA256) != request.DumpToolSHA256 {
		return false
	}
	if !strings.HasPrefix(job.SpecDigest, "sha256:") || len(job.SpecDigest) != 71 || strings.ToLower(job.SpecDigest) != job.SpecDigest {
		return false
	}
	version, versionErr := strconv.ParseUint(job.JobVersion, 10, 64)
	index, indexErr := strconv.ParseUint(job.JobModifyIndex, 10, 64)
	if versionErr != nil || strconv.FormatUint(version, 10) != job.JobVersion || indexErr != nil || index == 0 || strconv.FormatUint(index, 10) != job.JobModifyIndex {
		return false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(job.SpecDigest, "sha256:")); err != nil {
		return false
	}
	if _, err := hex.DecodeString(job.DatabaseBindingSHA256); err != nil || strings.ToLower(job.DatabaseBindingSHA256) != job.DatabaseBindingSHA256 {
		return false
	}
	if _, err := hex.DecodeString(request.DumpToolSHA256); err != nil {
		return false
	}
	seen := make(map[string]bool, len(job.AllocationIDs))
	for _, id := range job.AllocationIDs {
		if strings.TrimSpace(id) == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func sameMySQLSourceSnapshotPayload(payload map[string]interface{}, request MySQLSourceSnapshotRequest) bool {
	encoded, err := json.Marshal(request)
	if err != nil {
		return false
	}
	want, err := DecodeExactJSONObject(encoded)
	return err == nil && exactJSONEqual(payload, want)
}

func sameJSON(left, right []byte) bool {
	l, err := DecodeExactJSONObject(left)
	if err != nil {
		return false
	}
	r, err := DecodeExactJSONObject(right)
	return err == nil && exactJSONEqual(l, r)
}

func rejectMySQLSourceSnapshotIntent(ctx context.Context, tx pgx.Tx, source database.TargetIdentity) error {
	active, err := loadActiveDatabaseCatalog(ctx, tx)
	if err != nil {
		return err
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mysql_source_snapshot_intents WHERE source_key=$1)`, mysqlRuntimePhysicalKeyForCatalog(active.Catalog, source)).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return ErrMySQLSourceSnapshotFence
	}
	return nil
}
