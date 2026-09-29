package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

const mysqlDeployedDatabaseTargetsSchema = "norn.database-targets/v1"

type MySQLDeployedSourceBindingRequest struct {
	DeploymentID    string
	App             string
	SpecDigest      string
	Region          string
	NomadRegion     string
	ProfileID       string
	LogicalID       string
	CatalogRevision int64
	Source          database.TargetIdentity
}

type MySQLDeployedSourceBinding struct {
	OperationID           string
	AcceptanceIntentID    string
	DeploymentID          string
	App                   string
	SpecDigest            string
	Region                string
	NomadRegion           string
	DatabaseBindingSchema string
	DatabaseBindingSHA256 string
	CatalogRevision       int64
	ProfileID             string
	LogicalID             string
	Source                database.TargetIdentity
}

var ErrMySQLDeployedSourceBinding = errors.New("signed deployed MySQL source binding is unavailable")

type mysqlDeployedTargetSet struct {
	Schema          string                     `json:"schema"`
	ProfileID       string                     `json:"profileId"`
	CatalogRevision int64                      `json:"catalogRevision"`
	Targets         []mysqlDeployedNamedTarget `json:"targets"`
}

type mysqlDeployedNamedTarget struct {
	Name   string                  `json:"name"`
	Target database.TargetIdentity `json:"target"`
}

// VerifySignedDeployedMySQLSourceBinding proves that the selected source was
// one exact named target of a signed, successful deployment and that the
// selected Nomad region reached deployed state. It reads no mutable app spec;
// the signed deploy payload is target authority and its spec digest is checked
// against the terminal deployment row.
func (db *DB) VerifySignedDeployedMySQLSourceBinding(ctx context.Context, acceptance *PGOperationStore, request MySQLDeployedSourceBindingRequest) (MySQLDeployedSourceBinding, error) {
	if db == nil || db.Pool == nil || acceptance == nil || acceptance.db != db ||
		strings.TrimSpace(request.DeploymentID) == "" || strings.TrimSpace(request.App) == "" ||
		strings.TrimSpace(request.SpecDigest) == "" || strings.TrimSpace(request.Region) == "" || strings.TrimSpace(request.NomadRegion) == "" ||
		strings.TrimSpace(request.ProfileID) == "" || strings.TrimSpace(request.LogicalID) == "" || request.CatalogRevision <= 0 ||
		request.Source.Engine != database.EngineMySQL || request.Source.ServiceGeneration == 0 || request.Source.BindingGeneration == 0 {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	if !strings.HasPrefix(request.SpecDigest, "sha256:") || len(request.SpecDigest) != 71 || strings.ToLower(request.SpecDigest) != request.SpecDigest {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(request.SpecDigest, "sha256:")); err != nil {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	rows, err := db.Pool.Query(ctx, `SELECT operation_id FROM operation_acceptance_intents WHERE deployment_id=$1 ORDER BY operation_id LIMIT 2`, request.DeploymentID)
	if err != nil {
		return MySQLDeployedSourceBinding{}, err
	}
	var operationIDs []string
	for rows.Next() {
		var operationID string
		if rows.Scan(&operationID) != nil {
			rows.Close()
			return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
		}
		operationIDs = append(operationIDs, operationID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return MySQLDeployedSourceBinding{}, err
	}
	rows.Close()
	if len(operationIDs) != 1 {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, operationIDs[0])
	if err != nil {
		return MySQLDeployedSourceBinding{}, err
	}
	if accepted.Operation.Kind != "app.deploy" || accepted.Operation.App != request.App || accepted.Operation.Status != model.OperationSucceeded ||
		accepted.Deployment == nil || accepted.Deployment.ID != request.DeploymentID || accepted.Deployment.App != request.App ||
		accepted.Intent.DeploymentID != request.DeploymentID ||
		accepted.Deployment.Status != model.StatusDeployed || accepted.Deployment.SpecDigest != request.SpecDigest ||
		stringFromExactPayload(accepted.Operation.Payload, "deploymentId") != request.DeploymentID || stringFromExactPayload(accepted.Operation.Payload, "specDigest") != request.SpecDigest {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	var identityKind, identityResource string
	if err := db.Pool.QueryRow(ctx, `SELECT kind,resource FROM operation_request_identities WHERE operation_id=$1`, accepted.Operation.ID).Scan(&identityKind, &identityResource); err != nil || identityKind != "app.deploy" || identityResource != request.App {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	// Source quiescence currently has one writer-stop fence. Refuse a signed
	// deployment with another configured region until every writer can be
	// observed and fenced as one accepted set.
	if len(accepted.Regions) != 1 {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	configuredRegion := false
	for _, region := range accepted.Regions {
		if region.Name == request.Region && region.NomadRegion == request.NomadRegion {
			configuredRegion = true
			break
		}
	}
	var regionStatus model.DeployStatus
	if !configuredRegion || db.Pool.QueryRow(ctx, `SELECT status FROM deployment_regions WHERE deployment_id=$1 AND region=$2 AND nomad_region=$3`, request.DeploymentID, request.Region, request.NomadRegion).Scan(&regionStatus) != nil || regionStatus != model.StatusDeployed {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	raw, ok := accepted.Operation.Payload["databaseTargets"].(string)
	if !ok {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	targets, err := decodeMySQLDeployedTargetSet(raw)
	if err != nil || targets.ProfileID != request.ProfileID || targets.CatalogRevision != request.CatalogRevision {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	matched := false
	for _, target := range targets.Targets {
		if target.Name == request.LogicalID && target.Target == request.Source {
			matched = true
			break
		}
	}
	if !matched {
		return MySQLDeployedSourceBinding{}, ErrMySQLDeployedSourceBinding
	}
	digest := sha256.Sum256([]byte(mysqlDeployedDatabaseTargetsSchema + "\x00" + raw))
	return MySQLDeployedSourceBinding{OperationID: accepted.Operation.ID, AcceptanceIntentID: accepted.AcceptanceIntentID,
		DeploymentID: request.DeploymentID, App: request.App, SpecDigest: request.SpecDigest, Region: request.Region, NomadRegion: request.NomadRegion,
		DatabaseBindingSchema: mysqlDeployedDatabaseTargetsSchema, DatabaseBindingSHA256: hex.EncodeToString(digest[:]), CatalogRevision: request.CatalogRevision,
		ProfileID: request.ProfileID, LogicalID: request.LogicalID, Source: request.Source}, nil
}

func decodeMySQLDeployedTargetSet(raw string) (mysqlDeployedTargetSet, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var targets mysqlDeployedTargetSet
	var trailing json.RawMessage
	if decoder.Decode(&targets) != nil || decoder.Decode(&trailing) != io.EOF || targets.Schema != mysqlDeployedDatabaseTargetsSchema || targets.CatalogRevision <= 0 || strings.TrimSpace(targets.ProfileID) == "" || len(targets.Targets) == 0 {
		return mysqlDeployedTargetSet{}, ErrMySQLDeployedSourceBinding
	}
	seen := make(map[string]bool, len(targets.Targets))
	for _, target := range targets.Targets {
		if strings.TrimSpace(target.Name) == "" || seen[target.Name] || target.Target.Engine == "" || target.Target.ServiceGeneration == 0 || target.Target.BindingGeneration == 0 {
			return mysqlDeployedTargetSet{}, ErrMySQLDeployedSourceBinding
		}
		seen[target.Name] = true
	}
	return targets, nil
}

func stringFromExactPayload(payload map[string]interface{}, key string) string {
	value, _ := payload[key].(string)
	return value
}
