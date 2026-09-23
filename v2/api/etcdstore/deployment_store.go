package etcdstore

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// DeploymentStore is an etcd-backed store.DeploymentStore. The deployment
// aggregate is split across keys: prefix/deploy/d/<id> (the deployment, without
// its regions inlined), prefix/deploy/r/<id>/<region> (per-region intent) and
// prefix/deploy/s/<id>/<step> (pipeline steps). The composite writers commit the
// deployment, its regions and its durable operation together in a single etcd
// transaction. It passes the shared conformance suite.
type DeploymentStore struct {
	kv     clientv3.KV
	prefix string
}

// NewDeploymentStore returns an etcd deployment store rooted at prefix.
func NewDeploymentStore(kv clientv3.KV, prefix string) *DeploymentStore {
	return &DeploymentStore{kv: kv, prefix: prefix}
}

var _ store.DeploymentStore = (*DeploymentStore)(nil)

func (s *DeploymentStore) dKey(id string) string { return s.prefix + "/deploy/d/" + id }
func (s *DeploymentStore) dPrefix() string        { return s.prefix + "/deploy/d/" }
func (s *DeploymentStore) rKey(depID, region string) string {
	return s.prefix + "/deploy/r/" + depID + "/" + region
}
func (s *DeploymentStore) rPrefix(depID string) string { return s.prefix + "/deploy/r/" + depID + "/" }
func (s *DeploymentStore) sKey(depID, step string) string {
	return s.prefix + "/deploy/s/" + depID + "/" + step
}
func (s *DeploymentStore) sPrefix(depID string) string { return s.prefix + "/deploy/s/" + depID + "/" }
func (s *DeploymentStore) opKey(id string) string      { return s.prefix + "/ops/" + id }

func isTerminalDeploy(status model.DeployStatus) bool {
	return status == model.StatusDeployed || status == model.StatusFailed
}

func (s *DeploymentStore) putJSON(ctx context.Context, key string, value interface{}) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, key, string(raw))
	return err
}

func opPutJSON(key string, value interface{}) (clientv3.Op, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return clientv3.Op{}, err
	}
	return clientv3.OpPut(key, string(raw)), nil
}

func regionFromResolved(depID string, r model.ResolvedRegion) model.DeploymentRegion {
	return model.DeploymentRegion{
		DeploymentID:  depID,
		Region:        r.Name,
		NomadRegion:   r.NomadRegion,
		Status:        model.StatusQueued,
		DesiredWeight: r.TrafficWeight,
		ActiveWeight:  0,
	}
}

func (s *DeploymentStore) InsertDeployment(ctx context.Context, d *model.Deployment) error {
	stored := *d
	stored.Regions = nil
	return s.putJSON(ctx, s.dKey(d.ID), stored)
}

func (s *DeploymentStore) InsertDeploymentRegions(ctx context.Context, deploymentID string, regions []model.ResolvedRegion) error {
	for _, r := range regions {
		region := regionFromResolved(deploymentID, r)
		raw, err := json.Marshal(region)
		if err != nil {
			return err
		}
		// ON CONFLICT DO NOTHING: only write when the region key is absent.
		if _, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.CreateRevision(s.rKey(deploymentID, r.Name)), "=", 0)).
			Then(clientv3.OpPut(s.rKey(deploymentID, r.Name), string(raw))).
			Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *DeploymentStore) composite(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error {
	dcopy := *deployment
	dcopy.Regions = nil
	ops := make([]clientv3.Op, 0, 2+len(regions))
	dOp, err := opPutJSON(s.dKey(deployment.ID), dcopy)
	if err != nil {
		return err
	}
	ops = append(ops, dOp)
	for _, r := range regions {
		rOp, err := opPutJSON(s.rKey(deployment.ID, r.Name), regionFromResolved(deployment.ID, r))
		if err != nil {
			return err
		}
		ops = append(ops, rOp)
	}
	stored := *op
	normalizeOperation(&stored)
	oOp, err := opPutJSON(s.opKey(op.ID), stored)
	if err != nil {
		return err
	}
	ops = append(ops, oOp)
	_, err = s.kv.Txn(ctx).Then(ops...).Commit()
	return err
}

func (s *DeploymentStore) InsertDeploymentOperation(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error {
	return s.composite(ctx, deployment, regions, op)
}

func (s *DeploymentStore) InsertRollbackOperation(ctx context.Context, deployment *model.Deployment, regions []model.ResolvedRegion, op *model.Operation) error {
	return s.composite(ctx, deployment, regions, op)
}

func (s *DeploymentStore) loadDeployment(ctx context.Context, id string) (*model.Deployment, error) {
	resp, err := s.kv.Get(ctx, s.dKey(id))
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, ErrNotFound
	}
	var d model.Deployment
	if err := json.Unmarshal(resp.Kvs[0].Value, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *DeploymentStore) attachRegions(ctx context.Context, d *model.Deployment) error {
	regions, err := s.DeploymentRegions(ctx, d.ID)
	if err != nil {
		return err
	}
	d.Regions = regions
	return nil
}

func (s *DeploymentStore) UpdateDeploymentRegion(ctx context.Context, deploymentID, region string, status model.DeployStatus, evalID, lastError string, activeWeight int) error {
	resp, err := s.kv.Get(ctx, s.rKey(deploymentID, region))
	if err != nil {
		return err
	}
	if len(resp.Kvs) == 0 {
		return nil
	}
	var r model.DeploymentRegion
	if err := json.Unmarshal(resp.Kvs[0].Value, &r); err != nil {
		return err
	}
	r.Status = status
	if evalID != "" {
		r.EvalID = evalID
	}
	r.LastError = lastError
	r.ActiveWeight = activeWeight
	r.UpdatedAt = time.Now()
	return s.putJSON(ctx, s.rKey(deploymentID, region), r)
}

func (s *DeploymentStore) FailIncompleteDeploymentRegions(ctx context.Context, deploymentID, lastError string) error {
	regions, err := s.DeploymentRegions(ctx, deploymentID)
	if err != nil {
		return err
	}
	for _, r := range regions {
		if r.Status == model.StatusDeployed || r.Status == model.StatusFailed {
			continue
		}
		r.Status = model.StatusFailed
		r.ActiveWeight = 0
		if r.LastError == "" {
			r.LastError = lastError
		}
		r.UpdatedAt = time.Now()
		if err := s.putJSON(ctx, s.rKey(deploymentID, r.Region), r); err != nil {
			return err
		}
	}
	return nil
}

func (s *DeploymentStore) DeploymentRegions(ctx context.Context, deploymentID string) ([]model.DeploymentRegion, error) {
	resp, err := s.kv.Get(ctx, s.rPrefix(deploymentID), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var out []model.DeploymentRegion
	for _, kv := range resp.Kvs {
		var r model.DeploymentRegion
		if err := json.Unmarshal(kv.Value, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out, nil
}

func (s *DeploymentStore) UpdateDeployment(ctx context.Context, id string, status model.DeployStatus) error {
	d, err := s.loadDeployment(ctx, id)
	if err != nil {
		if err == ErrNotFound {
			return nil
		}
		return err
	}
	d.Status = status
	if isTerminalDeploy(status) {
		now := time.Now()
		d.FinishedAt = &now
	} else {
		d.FinishedAt = nil
	}
	return s.putJSON(ctx, s.dKey(id), *deploymentWithoutRegions(d))
}

func (s *DeploymentStore) UpdateDeploymentResult(ctx context.Context, d *model.Deployment) error {
	stored := *d
	stored.Regions = nil
	if isTerminalDeploy(d.Status) {
		now := time.Now()
		stored.FinishedAt = &now
	} else {
		stored.FinishedAt = nil
	}
	return s.putJSON(ctx, s.dKey(d.ID), stored)
}

func deploymentWithoutRegions(d *model.Deployment) *model.Deployment {
	clone := *d
	clone.Regions = nil
	return &clone
}

func (s *DeploymentStore) GetDeployment(ctx context.Context, id string) (*model.Deployment, error) {
	d, err := s.loadDeployment(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.attachRegions(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *DeploymentStore) scanDeployments(ctx context.Context) ([]model.Deployment, error) {
	resp, err := s.kv.Get(ctx, s.dPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]model.Deployment, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var d model.Deployment
		if err := json.Unmarshal(kv.Value, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *DeploymentStore) ListDeployments(ctx context.Context, app string, limit int) ([]model.Deployment, error) {
	if limit <= 0 {
		limit = 20
	}
	all, err := s.scanDeployments(ctx)
	if err != nil {
		return nil, err
	}
	var matched []model.Deployment
	for _, d := range all {
		if app != "" && d.App != app {
			continue
		}
		matched = append(matched, d)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].StartedAt.After(matched[j].StartedAt) })
	if len(matched) > limit {
		matched = matched[:limit]
	}
	for i := range matched {
		if err := s.attachRegions(ctx, &matched[i]); err != nil {
			return nil, err
		}
	}
	return matched, nil
}

func (s *DeploymentStore) firstSuccessful(ctx context.Context, match func(model.Deployment) bool) (*model.Deployment, error) {
	all, err := s.scanDeployments(ctx)
	if err != nil {
		return nil, err
	}
	var candidates []model.Deployment
	for _, d := range all {
		if d.Status == model.StatusDeployed && match(d) {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) == 0 {
		return nil, ErrNotFound
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].StartedAt.After(candidates[j].StartedAt) })
	winner := candidates[0]
	if err := s.attachRegions(ctx, &winner); err != nil {
		return nil, err
	}
	return &winner, nil
}

func (s *DeploymentStore) LastSuccessfulDeployment(ctx context.Context, app, excludeID string) (*model.Deployment, error) {
	return s.firstSuccessful(ctx, func(d model.Deployment) bool { return d.App == app && d.ID != excludeID })
}

func (s *DeploymentStore) LatestSuccessfulDeployment(ctx context.Context, app, environment string) (*model.Deployment, error) {
	return s.firstSuccessful(ctx, func(d model.Deployment) bool { return d.App == app && d.Environment == environment })
}

func (s *DeploymentStore) RecoverInFlightDeployments(ctx context.Context) error {
	all, err := s.scanDeployments(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, d := range all {
		if isTerminalDeploy(d.Status) {
			continue
		}
		regions, err := s.DeploymentRegions(ctx, d.ID)
		if err != nil {
			return err
		}
		for _, r := range regions {
			r.Status = model.StatusFailed
			r.ActiveWeight = 0
			if r.LastError == "" {
				r.LastError = "norn restarted during deployment"
			}
			r.UpdatedAt = now
			if err := s.putJSON(ctx, s.rKey(d.ID, r.Region), r); err != nil {
				return err
			}
		}
		d.Status = model.StatusFailed
		finished := now
		d.FinishedAt = &finished
		if err := s.putJSON(ctx, s.dKey(d.ID), d); err != nil {
			return err
		}
	}
	return nil
}

func (s *DeploymentStore) DeploymentMetrics(ctx context.Context) ([]store.DeploymentMetric, error) {
	all, err := s.scanDeployments(ctx)
	if err != nil {
		return nil, err
	}
	type key struct {
		app    string
		status model.DeployStatus
	}
	agg := map[key]*store.DeploymentMetric{}
	for _, d := range all {
		k := key{d.App, d.Status}
		m, ok := agg[k]
		if !ok {
			m = &store.DeploymentMetric{App: d.App, Status: d.Status}
			agg[k] = m
		}
		m.Count++
		if d.FinishedAt != nil {
			m.DurationSeconds += d.FinishedAt.Sub(d.StartedAt).Seconds()
		}
		if started := float64(d.StartedAt.Unix()); started > m.LastStartedUnix {
			m.LastStartedUnix = started
		}
	}
	out := make([]store.DeploymentMetric, 0, len(agg))
	for _, m := range agg {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].Status < out[j].Status
	})
	return out, nil
}

func (s *DeploymentStore) StartDeploymentStep(ctx context.Context, step model.DeploymentStep) error {
	if step.Metadata == nil {
		step.Metadata = map[string]interface{}{}
	}
	if step.StartedAt.IsZero() {
		step.StartedAt = time.Now()
	}
	if step.Status == "" {
		step.Status = model.DeploymentStepRunning
	}
	step.FinishedAt = nil
	step.DurationMs = 0
	// ON CONFLICT DO UPDATE: merge onto any existing step's metadata and reset
	// its finish, so a restarted step re-opens rather than duplicates.
	resp, err := s.kv.Get(ctx, s.sKey(step.DeploymentID, step.Step))
	if err != nil {
		return err
	}
	if len(resp.Kvs) > 0 {
		var existing model.DeploymentStep
		if err := json.Unmarshal(resp.Kvs[0].Value, &existing); err == nil {
			merged := map[string]interface{}{}
			for k, v := range existing.Metadata {
				merged[k] = v
			}
			for k, v := range step.Metadata {
				merged[k] = v
			}
			step.Metadata = merged
		}
	}
	return s.putJSON(ctx, s.sKey(step.DeploymentID, step.Step), step)
}

func (s *DeploymentStore) FinishDeploymentStep(ctx context.Context, deploymentID, step string, status model.DeploymentStepStatus, durationMs int64, message string, metadata map[string]interface{}) error {
	resp, err := s.kv.Get(ctx, s.sKey(deploymentID, step))
	if err != nil {
		return err
	}
	if len(resp.Kvs) == 0 {
		return nil
	}
	var stored model.DeploymentStep
	if err := json.Unmarshal(resp.Kvs[0].Value, &stored); err != nil {
		return err
	}
	stored.Status = status
	now := time.Now()
	stored.FinishedAt = &now
	stored.DurationMs = durationMs
	stored.Message = message
	if stored.Metadata == nil {
		stored.Metadata = map[string]interface{}{}
	}
	for k, v := range metadata {
		stored.Metadata[k] = v
	}
	return s.putJSON(ctx, s.sKey(deploymentID, step), stored)
}

func (s *DeploymentStore) ListDeploymentSteps(ctx context.Context, deploymentID string) ([]model.DeploymentStep, error) {
	resp, err := s.kv.Get(ctx, s.sPrefix(deploymentID), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var out []model.DeploymentStep
	for _, kv := range resp.Kvs {
		var step model.DeploymentStep
		if err := json.Unmarshal(kv.Value, &step); err != nil {
			return nil, err
		}
		out = append(out, step)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}
