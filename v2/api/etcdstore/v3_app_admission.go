package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// App admission keeps a bounded index of queued/running operations. The
// revisioned fence serializes acceptance across API replicas, while the
// exclusive pointer prevents later ordinary app work from bypassing a
// OneActiveMutablePerApp operation.
func (s *V3OperationStore) appAdmissionPrefix(app string) string {
	sum := sha256.Sum256([]byte(app))
	return s.prefix + "/v3/app-admission/" + hex.EncodeToString(sum[:]) + "/"
}

func (s *V3OperationStore) appAdmissionFenceKey(app string) string {
	return s.appAdmissionPrefix(app) + "fence"
}
func (s *V3OperationStore) appAdmissionInitializedKey(app string) string {
	return s.appAdmissionPrefix(app) + "initialized"
}
func (s *V3OperationStore) appAdmissionExclusiveKey(app string) string {
	return s.appAdmissionPrefix(app) + "exclusive"
}
func (s *V3OperationStore) appAdmissionActivePrefix(app string) string {
	return s.appAdmissionPrefix(app) + "active/"
}
func (s *V3OperationStore) appAdmissionActiveKey(app, operationID string) string {
	return s.appAdmissionActivePrefix(app) + operationID
}

type appAdmissionPlan struct {
	compares []clientv3.Cmp
	puts     []clientv3.Op
}

func (s *V3OperationStore) prepareAppAdmission(ctx context.Context, acceptance store.OperationAcceptance) (appAdmissionPlan, error) {
	op := acceptance.Operation
	if op.App == "" || op.Status != model.OperationQueued {
		if acceptance.Admission.OneActiveMutablePerApp {
			return appAdmissionPlan{}, &store.AcceptanceValidationError{Reason: "active-app admission requires a queued app operation"}
		}
		return appAdmissionPlan{}, nil
	}
	fenceKey, exclusiveKey := s.appAdmissionFenceKey(op.App), s.appAdmissionExclusiveKey(op.App)
	initializedKey := s.appAdmissionInitializedKey(op.App)
	initialized, err := s.kv.Get(ctx, initializedKey)
	if err != nil {
		return appAdmissionPlan{}, err
	}
	if len(initialized.Kvs) == 0 {
		if err := s.rejectPreIndexActiveAppOperations(ctx, op.App); err != nil {
			return appAdmissionPlan{}, err
		}
	}
	fence, err := s.kv.Get(ctx, fenceKey)
	if err != nil {
		return appAdmissionPlan{}, err
	}
	exclusive, err := s.kv.Get(ctx, exclusiveKey)
	if err != nil {
		return appAdmissionPlan{}, err
	}
	if len(exclusive.Kvs) != 0 {
		return appAdmissionPlan{}, &store.AcceptanceAdmissionError{App: op.App}
	}
	plan := appAdmissionPlan{compares: []clientv3.Cmp{
		clientv3.Compare(clientv3.CreateRevision(exclusiveKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.appAdmissionActiveKey(op.App, op.ID)), "=", 0),
	}}
	if len(initialized.Kvs) == 0 {
		plan.compares = append(plan.compares, clientv3.Compare(clientv3.CreateRevision(initializedKey), "=", 0))
		plan.puts = append(plan.puts, clientv3.OpPut(initializedKey, "v1"))
	} else if string(initialized.Kvs[0].Value) != "v1" {
		return appAdmissionPlan{}, fmt.Errorf("app admission initialization marker is invalid")
	} else {
		plan.compares = append(plan.compares, clientv3.Compare(clientv3.ModRevision(initializedKey), "=", initialized.Kvs[0].ModRevision))
	}
	if len(fence.Kvs) == 0 {
		plan.compares = append(plan.compares, clientv3.Compare(clientv3.CreateRevision(fenceKey), "=", 0))
	} else {
		plan.compares = append(plan.compares, clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fence.Kvs[0].ModRevision))
	}
	if acceptance.Admission.OneActiveMutablePerApp {
		active, err := s.kv.Get(ctx, s.appAdmissionActivePrefix(op.App), clientv3.WithPrefix(), clientv3.WithLimit(1))
		if err != nil {
			return appAdmissionPlan{}, err
		}
		if len(active.Kvs) != 0 {
			return appAdmissionPlan{}, &store.AcceptanceAdmissionError{App: op.App}
		}
	}
	plan.puts = append(plan.puts,
		clientv3.OpPut(fenceKey, uuid.NewString()),
		clientv3.OpPut(s.appAdmissionActiveKey(op.App, op.ID), op.ID),
	)
	if acceptance.Admission.OneActiveMutablePerApp {
		plan.puts = append(plan.puts, clientv3.OpPut(exclusiveKey, op.ID))
	}
	return plan, nil
}

// The first indexed app acceptance can only start after any prior active
// operations have drained. This is a one-time scan per app; the marker is
// created atomically with the first indexed acceptance. Mixed-version API
// writers must be stopped before enabling this adapter.
func (s *V3OperationStore) rejectPreIndexActiveAppOperations(ctx context.Context, app string) error {
	prefix := s.prefix + "/v3/operations/"
	cursor, end := prefix, clientv3.GetPrefixRangeEnd(prefix)
	var revision int64
	for {
		options := []clientv3.OpOption{clientv3.WithRange(end), clientv3.WithLimit(256)}
		if revision != 0 {
			options = append(options, clientv3.WithRev(revision))
		}
		response, err := s.kv.Get(ctx, cursor, options...)
		if err != nil {
			return err
		}
		if revision == 0 {
			revision = response.Header.Revision
		}
		for _, item := range response.Kvs {
			var record v3Record
			if err := decodeV3Record(item.Value, &record); err != nil {
				return fmt.Errorf("decode pre-index operation: %w", err)
			}
			if record.Operation.App == app && (!record.Operation.Status.Terminal() || record.Operation.Metadata["manualRecoveryRequired"] == true || record.Operation.Metadata["externalEffectRecoveryPending"] == true) {
				return &store.AcceptanceAdmissionError{App: app}
			}
		}
		if !response.More || len(response.Kvs) == 0 {
			return nil
		}
		cursor = string(response.Kvs[len(response.Kvs)-1].Key) + "\x00"
	}
}

func (s *V3OperationStore) releaseAppAdmission(ctx context.Context, op model.Operation) ([]clientv3.Cmp, []clientv3.Op, error) {
	if op.App == "" || !op.Status.Terminal() || op.Metadata["manualRecoveryRequired"] == true || op.Metadata["externalEffectRecoveryPending"] == true {
		return nil, nil, nil
	}
	activeKey := s.appAdmissionActiveKey(op.App, op.ID)
	active, err := s.kv.Get(ctx, activeKey)
	if err != nil {
		return nil, nil, err
	}
	if len(active.Kvs) == 0 {
		// Historical operations accepted before this index was introduced have
		// nothing to release.
		return nil, nil, nil
	}
	if string(active.Kvs[0].Value) != op.ID {
		return nil, nil, fmt.Errorf("app admission active operation identity differs")
	}
	compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(activeKey), "=", active.Kvs[0].ModRevision)}
	ops := []clientv3.Op{clientv3.OpDelete(activeKey)}
	exclusiveKey := s.appAdmissionExclusiveKey(op.App)
	exclusive, err := s.kv.Get(ctx, exclusiveKey)
	if err != nil {
		return nil, nil, err
	}
	if len(exclusive.Kvs) != 0 && string(exclusive.Kvs[0].Value) == op.ID {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(exclusiveKey), "=", exclusive.Kvs[0].ModRevision))
		ops = append(ops, clientv3.OpDelete(exclusiveKey))
	} else if len(exclusive.Kvs) != 0 {
		return nil, nil, fmt.Errorf("app admission exclusive operation identity differs")
	}
	return compares, ops, nil
}
