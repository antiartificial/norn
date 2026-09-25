package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type MySQLSourceJobObserver interface {
	ObserveMySQLSourceJob(context.Context, nomad.MySQLSourceJobObservationRequest) (nomad.MySQLSourceJobObservation, error)
}

type MySQLSourceSnapshotAdmissionRequest struct {
	Binding        MySQLDeployedSourceBindingRequest
	Maintenance    database.MySQLMaintenanceCredentials
	DumpToolSHA256 string
}

// MySQLSourceSnapshotAcceptanceInput contains only the operator's stable
// selection and replay identity. The signed job revision and allocation IDs
// are always derived by the server from Nomad, never supplied by this caller.
type MySQLSourceSnapshotAcceptanceInput struct {
	Selection MySQLSourceSnapshotAdmissionRequest
	Actor     OperationActor
	Key       string
	Audit     AcceptanceAuditContext
}

// AcceptPrivateMySQLSourceSnapshot makes the observation and acceptance one
// private entry point. An exact replay returns the original signed operation
// even if Nomad has since changed; execution will refuse that stale revision.
// The normal operation store provides the atomic request-key conflict check.
func (db *DB) AcceptPrivateMySQLSourceSnapshot(ctx context.Context, acceptance *PGOperationStore, observer MySQLSourceJobObserver, input MySQLSourceSnapshotAcceptanceInput) (AcceptedOperation, error) {
	if db == nil || acceptance == nil || acceptance.db != db || observer == nil || strings.TrimSpace(input.Key) == "" ||
		strings.TrimSpace(input.Actor.Issuer) == "" || strings.TrimSpace(input.Actor.Subject) == "" {
		return AcceptedOperation{}, ErrMySQLSourceSnapshotFence
	}
	authority, err := acceptance.Authority(ctx)
	if err != nil {
		return AcceptedOperation{}, err
	}
	selection := input.Selection
	identity := OperationRequestIdentity{Authority: authority, Actor: input.Actor, Kind: MySQLSourceSnapshotOperationKind,
		Resource: "app/" + selection.Binding.App + "/database/" + selection.Binding.LogicalID, Key: input.Key}
	if existing, err := acceptance.ResolveIdentity(ctx, identity); err == nil {
		if !sameMySQLSourceSnapshotSelection(existing, selection) {
			return AcceptedOperation{}, &AcceptanceConflictError{Identity: identity}
		}
		return existing, nil
	} else if !errors.Is(err, ErrAcceptanceNotFound) {
		return AcceptedOperation{}, err
	}
	request, err := db.BuildMySQLSourceSnapshotRequest(ctx, acceptance, observer, selection)
	if err != nil {
		return AcceptedOperation{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return AcceptedOperation{}, err
	}
	var payload map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return AcceptedOperation{}, err
	}
	entry := OperationAcceptance{Identity: identity, Audit: input.Audit,
		Operation: model.Operation{ID: uuid.NewString(), Kind: MySQLSourceSnapshotOperationKind, App: request.JobIdentity.App,
			Ref: request.JobIdentity.DeploymentID, Status: model.OperationQueued, Risk: "high", Source: "private-mysql-source-snapshot",
			MaxAttempts: 1, Payload: payload}}
	entry.Fingerprint, err = CanonicalOperationRequestFingerprint(entry)
	if err != nil {
		return AcceptedOperation{}, err
	}
	return acceptance.Accept(ctx, entry)
}

func sameMySQLSourceSnapshotSelection(existing AcceptedOperation, selection MySQLSourceSnapshotAdmissionRequest) bool {
	if existing.Operation.Kind != MySQLSourceSnapshotOperationKind || existing.Operation.App != selection.Binding.App ||
		existing.Operation.Status == model.OperationCanceled || existing.Operation.MaxAttempts != 1 {
		return false
	}
	var request MySQLSourceSnapshotRequest
	encoded, err := json.Marshal(existing.Operation.Payload)
	if err != nil || decodeStrictAcceptanceJSON(encoded, &request) != nil || !validMySQLSourceSnapshotRequest(request) {
		return false
	}
	binding := selection.Binding
	return request.CatalogRevision == binding.CatalogRevision && request.ProfileID == binding.ProfileID &&
		request.LogicalID == binding.LogicalID && request.Source == binding.Source && request.Maintenance == selection.Maintenance &&
		request.DumpToolSHA256 == selection.DumpToolSHA256 && request.JobIdentity.App == binding.App &&
		request.JobIdentity.DeploymentID == binding.DeploymentID && request.JobIdentity.SpecDigest == binding.SpecDigest &&
		request.JobIdentity.Region == binding.Region && request.JobIdentity.NomadRegion == binding.NomadRegion
}

// BuildMySQLSourceSnapshotRequest is the private admission boundary that
// combines signed deployment/source authority with a fresh Nomad observation.
// Its result is suitable for exact operation acceptance; it performs no
// external mutation.
func (db *DB) BuildMySQLSourceSnapshotRequest(ctx context.Context, acceptance *PGOperationStore, observer MySQLSourceJobObserver, input MySQLSourceSnapshotAdmissionRequest) (MySQLSourceSnapshotRequest, error) {
	if observer == nil {
		return MySQLSourceSnapshotRequest{}, ErrMySQLSourceSnapshotFence
	}
	binding, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptance, input.Binding)
	if err != nil {
		return MySQLSourceSnapshotRequest{}, err
	}
	active, err := db.ActiveDatabaseCatalog(ctx)
	if err != nil || active.Revision != binding.CatalogRevision {
		return MySQLSourceSnapshotRequest{}, ErrMySQLSourceSnapshotFence
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		return MySQLSourceSnapshotRequest{}, ErrMySQLSourceSnapshotFence
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: binding.ProfileID,
		Purpose: database.PurposeApplication, LogicalResourceID: binding.LogicalID, Expected: &binding.Source})
	if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != input.Maintenance ||
		input.Maintenance.SnapshotRole == "" || input.Maintenance.SnapshotCredentialRef == "" {
		return MySQLSourceSnapshotRequest{}, ErrMySQLSourceSnapshotFence
	}
	want := nomad.MySQLSourceJobObservationRequest{
		App: binding.App, DeploymentID: binding.DeploymentID, SpecDigest: binding.SpecDigest, Region: binding.Region,
		NomadRegion: binding.NomadRegion, DatabaseBindingSchema: binding.DatabaseBindingSchema,
		DatabaseBindingSHA256: binding.DatabaseBindingSHA256, DatabaseCatalogRevision: strconv.FormatInt(binding.CatalogRevision, 10),
	}
	observed, err := observer.ObserveMySQLSourceJob(ctx, want)
	if err != nil || !sameMySQLSourceJobObservation(want, observed) {
		return MySQLSourceSnapshotRequest{}, ErrMySQLSourceSnapshotFence
	}
	request := MySQLSourceSnapshotRequest{
		CatalogRevision: binding.CatalogRevision, ProfileID: binding.ProfileID, LogicalID: binding.LogicalID, Source: binding.Source,
		Maintenance: input.Maintenance, DumpToolSHA256: input.DumpToolSHA256,
		JobIdentity: MySQLSourceSnapshotJobIdentity{
			App: observed.App, DeploymentID: observed.DeploymentID, SpecDigest: observed.SpecDigest, Region: observed.Region,
			NomadRegion: observed.NomadRegion, JobID: observed.JobID, JobVersion: observed.JobVersion,
			JobModifyIndex: observed.JobModifyIndex, AllocationIDs: append([]string(nil), observed.AllocationIDs...),
			DatabaseBindingSchema: observed.DatabaseBindingSchema, DatabaseBindingSHA256: observed.DatabaseBindingSHA256,
			DatabaseCatalogRevision: observed.DatabaseCatalogRevision,
		},
	}
	if !validMySQLSourceSnapshotRequest(request) {
		return MySQLSourceSnapshotRequest{}, ErrMySQLSourceSnapshotFence
	}
	return request, nil
}

func sameMySQLSourceJobObservation(want nomad.MySQLSourceJobObservationRequest, got nomad.MySQLSourceJobObservation) bool {
	return got.App == want.App && got.JobID == want.App && got.DeploymentID == want.DeploymentID && got.SpecDigest == want.SpecDigest &&
		got.Region == want.Region && got.NomadRegion == want.NomadRegion && got.DatabaseBindingSchema == want.DatabaseBindingSchema &&
		got.DatabaseBindingSHA256 == want.DatabaseBindingSHA256 && got.DatabaseCatalogRevision == want.DatabaseCatalogRevision
}
