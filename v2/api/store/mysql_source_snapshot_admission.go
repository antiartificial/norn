package store

import (
	"context"
	"strconv"

	"norn/v2/api/database"
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
