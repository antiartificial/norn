package store

import (
	"context"
	"strconv"

	"norn/v2/api/artifactstore"
	"norn/v2/api/database"
	"norn/v2/api/nomad"
)

// MySQLSourceSnapshotLiveInspection adds advisory observations to the signed
// control view. A true value is a read at inspection time, not permission to
// repeat a mutation, acknowledge an ambiguous effect, or release the fence.
type MySQLSourceSnapshotLiveInspection struct {
	MySQLSourceSnapshotInspection
	NomadStoppedVerified   bool `json:"nomadStoppedVerified"`
	RuntimeAccountLocked   bool `json:"runtimeAccountLocked"`
	RetainedObjectVerified bool `json:"retainedObjectVerified"`
}

type MySQLSourceArtifactVerifier interface {
	Verify(context.Context, artifactstore.Descriptor) error
}

// InspectPrivateMySQLSourceSnapshotLive observes an exact signed source job,
// catalog-bound runtime account and content-addressed object where those
// observations are relevant. Provider verification streams the full object;
// callers must opt in explicitly and bound their context/deadline.
func (db *DB) InspectPrivateMySQLSourceSnapshotLive(ctx context.Context, acceptance *PGOperationStore, operationID string,
	observer MySQLSourceStoppedObserver, secrets database.SecretSource, objects MySQLSourceArtifactVerifier) (MySQLSourceSnapshotLiveInspection, error) {
	if observer == nil || secrets == nil || objects == nil {
		return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
	}
	control, err := db.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, operationID)
	if err != nil {
		return MySQLSourceSnapshotLiveInspection{}, err
	}
	accepted, err := acceptance.VerifyAcceptedOperation(ctx, operationID)
	if err != nil {
		return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
	}
	request, err := sourceSnapshotRequestFromAccepted(accepted)
	if err != nil {
		return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
	}
	result := MySQLSourceSnapshotLiveInspection{MySQLSourceSnapshotInspection: control}
	if sourceStateAtLeastStopIntended(control.IntentState) {
		version, versionErr := strconv.ParseUint(request.JobIdentity.JobVersion, 10, 64)
		index, indexErr := strconv.ParseUint(request.JobIdentity.JobModifyIndex, 10, 64)
		if versionErr == nil && indexErr == nil {
			job := request.JobIdentity
			result.NomadStoppedVerified = observer.ObserveStoppedMySQLSourceJob(ctx, nomad.CASStopJobRequest{
				JobID: job.JobID, Region: job.NomadRegion, JobVersion: version, JobModifyIndex: index,
				AllocationIDs: append([]string(nil), job.AllocationIDs...), DeploymentID: job.DeploymentID,
				SpecDigest: job.SpecDigest, DatabaseBindingSchema: job.DatabaseBindingSchema,
				DatabaseBindingSHA256: job.DatabaseBindingSHA256, DatabaseCatalogRevision: job.DatabaseCatalogRevision,
			}) == nil
		}
	}
	if sourceStateAtLeastLockIntended(control.IntentState) {
		catalog, err := db.DatabaseCatalogRevision(ctx, request.CatalogRevision)
		if err != nil {
			return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
		}
		resolver, err := database.NewResolver(catalog.Catalog)
		if err != nil {
			return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
		}
		resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: request.ProfileID,
			Purpose: database.PurposeApplication, LogicalResourceID: request.LogicalID, Expected: &request.Source})
		if err != nil || resolved.MySQLMaintenance == nil || *resolved.MySQLMaintenance != request.Maintenance {
			return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
		}
		result.RuntimeAccountLocked = database.InspectMySQLRuntimeAccountLockForRestore(ctx, resolved, request.Maintenance, secrets) == nil
	}
	if control.IntentState == "publish-intended" {
		stage, err := db.LoadSignedMySQLSourceArtifactReceipt(ctx, acceptance, operationID)
		if err != nil {
			return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
		}
		descriptor, err := descriptorForStagedMySQLArtifact(stage)
		if err != nil {
			return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
		}
		result.RetainedObjectVerified = objects.Verify(ctx, descriptor) == nil
	} else if control.IntentState == "retained-proved" {
		retained, err := db.LoadSignedMySQLSourceArtifactRetentionReceipt(ctx, acceptance, operationID)
		if err != nil {
			return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
		}
		result.RetainedObjectVerified = objects.Verify(ctx, retained.Receipt.Artifact) == nil
	}
	latest, err := db.InspectPrivateMySQLSourceSnapshot(ctx, acceptance, operationID)
	if err != nil || latest.OperationStatus != control.OperationStatus || latest.IntentState != control.IntentState ||
		latest.RuntimeFenceHeld != control.RuntimeFenceHeld || latest.StageReceiptVerified != control.StageReceiptVerified ||
		latest.RetentionReceiptVerified != control.RetentionReceiptVerified {
		return MySQLSourceSnapshotLiveInspection{}, ErrMySQLSourceSnapshotInspection
	}
	result.MySQLSourceSnapshotInspection = latest
	return result, nil
}

func sourceStateAtLeastStopIntended(state string) bool {
	switch state {
	case "stop-intended", "stop-proved", "lock-intended", "lock-proved", "stage-intended", "stage-proved", "publish-intended", "retained-proved":
		return true
	default:
		return false
	}
}

func sourceStateAtLeastLockIntended(state string) bool {
	switch state {
	case "lock-intended", "lock-proved", "stage-intended", "stage-proved", "publish-intended", "retained-proved":
		return true
	default:
		return false
	}
}
