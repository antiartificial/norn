package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
)

var (
	_ effect.Store         = (*V3CanaryEffectReservations)(nil)
	_ effect.RecoveryStore = (*V3CanaryEffectReservations)(nil)
)

func (s *V3CanaryEffectReservations) loadToken(ctx context.Context, token effect.Token) (effect.Record, string, int64, error) {
	if s == nil || s.operations == nil || token.EffectID == "" || token.Generation <= 0 {
		return effect.Record{}, "", 0, effect.ErrStaleToken
	}
	index, err := s.operations.kv.Get(ctx, s.tokenKey(token.EffectID))
	if err != nil {
		return effect.Record{}, "", 0, err
	}
	if len(index.Kvs) != 1 {
		return effect.Record{}, "", 0, effect.ErrStaleToken
	}
	key := string(index.Kvs[0].Value)
	if !strings.HasPrefix(key, s.operations.prefix+"/v3/effects/canary/") {
		return effect.Record{}, "", 0, fmt.Errorf("canary effect token index names a foreign namespace")
	}
	result, err := s.operations.kv.Get(ctx, key)
	if err != nil {
		return effect.Record{}, "", 0, err
	}
	if len(result.Kvs) != 1 {
		return effect.Record{}, "", 0, fmt.Errorf("canary effect token index has no record")
	}
	var record effect.Record
	if err := json.Unmarshal(result.Kvs[0].Value, &record); err != nil {
		return effect.Record{}, "", 0, fmt.Errorf("decode canary effect record: %w", err)
	}
	if record.Token != token || record.Reservation.Authority != s.operations.authority || key != s.effectKey(record.Reservation) {
		return effect.Record{}, "", 0, effect.ErrStaleToken
	}
	return record, key, result.Kvs[0].ModRevision, nil
}

func (s *V3CanaryEffectReservations) MarkLaunched(ctx context.Context, token effect.Token, identity effect.ExecutionIdentity) error {
	record, key, rev, err := s.loadToken(ctx, token)
	if err != nil {
		return err
	}
	if record.Lifecycle != effect.LifecycleReserved || identity.Supervisor != record.Reservation.Supervisor ||
		identity.SupervisorExecutionID != record.Reservation.SupervisorExecutionID || identity.RuntimeInstanceID == "" {
		return effect.ErrStaleToken
	}
	record.Lifecycle, record.Execution = effect.LifecycleLaunched, identity
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	txn, err := s.operations.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(key), "=", rev),
		clientv3.Compare(clientv3.Value(s.gateKey(effectApp(record.Reservation.Resource))), "=", key),
	).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return effect.ErrStaleToken
	}
	return nil
}

func effectApp(resource string) string {
	parts := strings.Split(resource, "/")
	if len(parts) >= 3 && parts[0] == "app" {
		return parts[1]
	}
	return ""
}

func validateCanaryVerification(record effect.Record, v effect.Verification) error {
	if v.InputDigest != record.Reservation.InputDigest || v.SupervisorExecutionID != record.Reservation.SupervisorExecutionID ||
		v.EvidenceSource == "" || v.EvidenceReference == "" || v.ObservedAt.IsZero() {
		return fmt.Errorf("canary effect evidence is not bound to its reserved execution")
	}
	if record.Execution.RuntimeInstanceID != "" && v.RuntimeInstanceID != record.Execution.RuntimeInstanceID {
		return fmt.Errorf("canary effect runtime does not match launch")
	}
	return nil
}

func (s *V3CanaryEffectReservations) Complete(ctx context.Context, token effect.Token, completion effect.Completion) error {
	record, key, rev, err := s.loadToken(ctx, token)
	if err != nil {
		return err
	}
	if record.Lifecycle != effect.LifecycleReserved && record.Lifecycle != effect.LifecycleLaunched {
		return effect.ErrStaleToken
	}
	v := completion.Verification
	if err := validateCanaryVerification(record, v); err != nil {
		return err
	}
	if v.RuntimeInstanceID == "" ||
		(completion.Outcome == effect.OutcomeSucceeded && (v.Decision != effect.VerificationSucceeded || v.ResultDigest == "" || v.ResultReference == "")) ||
		(completion.Outcome == effect.OutcomeFailed && v.Decision != effect.VerificationFailed && v.Decision != effect.VerificationFailedRepeatSafe) ||
		(completion.Outcome != effect.OutcomeSucceeded && completion.Outcome != effect.OutcomeFailed) {
		return fmt.Errorf("canary effect completion outcome or evidence is invalid")
	}
	record.Lifecycle = effect.LifecycleCompleted
	record.Completion = &completion
	if record.Execution.RuntimeInstanceID == "" {
		record.Execution = effect.ExecutionIdentity{Supervisor: record.Reservation.Supervisor, SupervisorExecutionID: record.Reservation.SupervisorExecutionID, RuntimeInstanceID: v.RuntimeInstanceID}
	}
	return s.terminalize(ctx, key, rev, record)
}

func (s *V3CanaryEffectReservations) Resolve(ctx context.Context, token effect.Token, resolution effect.Resolution) error {
	record, key, rev, err := s.loadToken(ctx, token)
	if err != nil {
		return err
	}
	if record.Lifecycle != effect.LifecycleReserved && record.Lifecycle != effect.LifecycleLaunched {
		return effect.ErrStaleToken
	}
	v := resolution.Verification
	if err := validateCanaryVerification(record, v); err != nil {
		return err
	}
	if resolution.Decision != v.Decision ||
		(resolution.Decision != effect.VerificationNeverLaunched && resolution.Decision != effect.VerificationStoppedRepeatSafe) ||
		(resolution.Decision == effect.VerificationNeverLaunched && (v.RuntimeInstanceID != "" || record.Lifecycle == effect.LifecycleLaunched || record.Execution.RuntimeInstanceID != "")) ||
		(resolution.Decision == effect.VerificationStoppedRepeatSafe && v.RuntimeInstanceID == "") {
		return fmt.Errorf("canary effect resolution is not repeat safe")
	}
	record.Lifecycle = effect.LifecycleResolved
	return s.terminalize(ctx, key, rev, record)
}

func (s *V3CanaryEffectReservations) terminalize(ctx context.Context, key string, rev int64, record effect.Record) error {
	app := effectApp(record.Reservation.Resource)
	if app == "" {
		return fmt.Errorf("canary effect has no app gate")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	gate := s.gateKey(app)
	txn, err := s.operations.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(key), "=", rev),
		clientv3.Compare(clientv3.Value(gate), "=", key),
	).Then(clientv3.OpPut(key, string(encoded)), clientv3.OpDelete(gate)).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return effect.ErrStaleToken
	}
	return nil
}

func (s *V3CanaryEffectReservations) UnresolvedForResource(ctx context.Context, authority, resource string) (effect.Record, bool, error) {
	if s == nil || s.operations == nil || authority != s.operations.authority {
		return effect.Record{}, false, fmt.Errorf("canary effect authority does not match")
	}
	app := effectApp(resource)
	if app == "" {
		return effect.Record{}, false, fmt.Errorf("canary effect resource has no app")
	}
	gate, err := s.operations.kv.Get(ctx, s.gateKey(app))
	if err != nil {
		return effect.Record{}, false, err
	}
	if len(gate.Kvs) == 0 {
		return effect.Record{}, false, nil
	}
	key := string(gate.Kvs[0].Value)
	if !strings.HasPrefix(key, s.operations.prefix+"/v3/effects/canary/") {
		return effect.Record{}, false, fmt.Errorf("canary app gate names an unsupported effect")
	}
	value, err := s.operations.kv.Get(ctx, key)
	if err != nil {
		return effect.Record{}, false, err
	}
	if len(value.Kvs) != 1 {
		return effect.Record{}, false, fmt.Errorf("canary app gate has no effect record")
	}
	var record effect.Record
	if err := json.Unmarshal(value.Kvs[0].Value, &record); err != nil {
		return effect.Record{}, false, err
	}
	if record.Reservation.Authority != authority || effectApp(record.Reservation.Resource) != app ||
		(record.Lifecycle != effect.LifecycleReserved && record.Lifecycle != effect.LifecycleLaunched) || key != s.effectKey(record.Reservation) {
		return effect.Record{}, false, fmt.Errorf("canary app gate and effect record disagree")
	}
	return record, true, nil
}
