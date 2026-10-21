package main

import (
	"context"
	"fmt"
	"strings"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	"norn/v2/api/worker"
)

const (
	etcdScaleWorkerEnv = "NORN_ETCD_SCALE_WORKER"
	etcdScaleHTTPEnv   = "NORN_ETCD_SCALE_HTTP"
)

type etcdScaleRuntime struct {
	worker *worker.OperationWorker
	pipe   *pipeline.Pipeline
	store  *etcdstore.V3OperationStore
	env    string
	nomad  *nomad.Client
	// resolveTarget is injectable only for local admission tests; production
	// always resolves the completed active route from the etcd authority.
	resolveTarget func(context.Context, string, string, string) (etcdstore.FleetScaleTarget, error)
}

func newEtcdScaleRuntime(cfg *config.Config, operations *etcdstore.V3OperationStore) (*etcdScaleRuntime, error) {
	if cfg == nil || operations == nil || strings.TrimSpace(cfg.NomadAddr) == "" || strings.TrimSpace(cfg.AppsDir) == "" {
		return nil, fmt.Errorf("etcd scale requires Nomad, app catalog, and operation store")
	}
	client, err := nomad.NewClient(cfg.NomadAddr)
	if err != nil {
		return nil, err
	}
	reservations, err := etcdstore.NewV3ScaleEffectReservations(operations)
	if err != nil {
		return nil, err
	}
	effects, err := pipeline.NewNomadScaleEffectsWithStore(reservations, client)
	if err != nil {
		return nil, err
	}
	p := &pipeline.Pipeline{OperationStore: operations, Nomad: client, ScaleEffects: effects, FencedScaleIntent: operations}
	runtime := &etcdScaleRuntime{pipe: p, store: operations, env: cfg.EnvironmentID(), nomad: client, resolveTarget: operations.ActiveFleetScaleTarget}
	runtime.worker = worker.NewOperationWorkerForKinds(operations, runtime, []string{"app.scale"})
	return runtime, nil
}

func (e *etcdScaleRuntime) ExecuteOperation(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
	return nil, fmt.Errorf("etcd scale requires the claimed app lock")
}

func (e *etcdScaleRuntime) ExecuteOperationWithAppLock(ctx context.Context, op *model.Operation, claim store.OperationClaim, lock store.AppOperationLock) (*pipeline.OperationResult, error) {
	if e == nil || e.store == nil || e.pipe == nil || op == nil || op.Kind != "app.scale" || lock == nil || lock.Fence() == "" || lock.Context().Err() != nil {
		return nil, fmt.Errorf("claimed etcd scale is unavailable")
	}
	region, _ := op.Payload["region"].(string)
	target, err := e.resolveTarget(ctx, op.App, e.env, region)
	if err != nil || target.JobID != op.Payload["nomadJobId"] || target.DeploymentID != op.Payload["deploymentId"] || target.SpecDigest != op.Payload["specDigest"] || target.NomadNamespace != op.Payload["nomadNamespace"] {
		return nil, fmt.Errorf("claimed scale target differs from the active Fleet route: %v", err)
	}
	return e.pipe.ExecuteOperationWithAppLock(ctx, op, claim, lock)
}
