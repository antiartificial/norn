package store

import (
	"context"

	"norn/v2/api/model"
)

// DeploymentStore is the backend-neutral control boundary for the deployment
// aggregate: the deployment record, its per-region intent and its ordered
// pipeline steps, plus the composite writers that commit a deployment together
// with its durable operation receipt in one transaction.
//
// It is the second domain seam extracted for Norn v3 (roadmap M1 / P5), a
// sibling to OperationStore. Callers depend on this interface rather than a
// concrete *DB so an etcd-backed adapter can be introduced later. The
// PostgreSQL adapter (*DB) is the current sole implementation; the compile-time
// assertion below keeps them in lockstep, and any second implementation must
// pass the shared conformance suite in deployment_store_conformance_test.go.
//
// The composite InsertDeploymentOperation / InsertRollbackOperation belong here
// (not on OperationStore): the deployment is the aggregate root being created,
// and its durable operation is committed atomically alongside it.
type DeploymentStore interface {
	InsertDeployment(ctx context.Context, d *model.Deployment) error
	InsertDeploymentRegions(ctx context.Context, deploymentID string, regions []model.ResolvedRegion) error
	// InsertDeploymentOperation atomically commits a queued deployment, its
	// regional intent and its durable operation, so an idempotent request can
	// never leave an orphaned deployment record.
	InsertDeploymentOperation(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error
	// InsertRollbackOperation is the same atomic boundary for a rollback.
	InsertRollbackOperation(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error

	UpdateDeployment(ctx context.Context, id string, status model.DeployStatus) error
	UpdateDeploymentResult(ctx context.Context, d *model.Deployment) error
	UpdateDeploymentRegion(ctx context.Context, deploymentID, region string, status model.DeployStatus, evalID, lastError string, activeWeight int) error
	// FailIncompleteDeploymentRegions closes every region that did not reach a
	// terminal state, keeping active_weight at zero.
	FailIncompleteDeploymentRegions(ctx context.Context, deploymentID, lastError string) error
	DeploymentRegions(ctx context.Context, deploymentID string) ([]model.DeploymentRegion, error)

	GetDeployment(ctx context.Context, id string) (*model.Deployment, error)
	ListDeployments(ctx context.Context, app string, limit int) ([]model.Deployment, error)
	LastSuccessfulDeployment(ctx context.Context, app, excludeID string) (*model.Deployment, error)
	// LatestSuccessfulDeployment is the authoritative active deployment for a
	// control-plane environment; failed or queued rows never displace it.
	LatestSuccessfulDeployment(ctx context.Context, app, environment string) (*model.Deployment, error)
	// RecoverInFlightDeployments fails every non-terminal deployment (and its
	// regions) on executor restart.
	RecoverInFlightDeployments(ctx context.Context) error
	DeploymentMetrics(ctx context.Context) ([]DeploymentMetric, error)

	StartDeploymentStep(ctx context.Context, step model.DeploymentStep) error
	FinishDeploymentStep(ctx context.Context, deploymentID, step string, status model.DeploymentStepStatus, durationMs int64, message string, metadata map[string]interface{}) error
	ListDeploymentSteps(ctx context.Context, deploymentID string) ([]model.DeploymentStep, error)
}

// Compile-time proof that the PostgreSQL adapter satisfies the deployment
// boundary. A future etcd adapter adds its own assertion here.
var _ DeploymentStore = (*DB)(nil)
