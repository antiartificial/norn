// Package controlstore selects and constructs the backend for Norn's
// control-plane store boundaries. The same ControlStore contract is satisfied by
// the PostgreSQL adapter (*store.DB) and by the etcd adapters (etcdstore), so the
// control plane can be pointed at either — the mechanism v3 needs for a fresh HA
// Fleet on etcd with no control PostgreSQL.
//
// Every extracted domain boundary now has an etcd adapter and is included here:
// the five core boundaries (operations, deployments, events, Fleet attempts,
// evidence), the auth aggregate (identity + exec sessions, with atomic
// revoke-plus-cancel), notifications, webhooks, cron state, function executions,
// recovery drills, access patterns, GitHub dispatches, beacon incidents and
// control-backend readiness. Each passes the same conformance suite on both
// backends. The etcd backend remains experimental pending live-Fleet bootstrap/
// quorum/restore qualification; the PostgreSQL default is byte-for-byte unchanged.
// Migrating handler/pipeline consumers off concrete *store.DB onto these
// interfaces is a separate follow-on refactor.
package controlstore

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/etcdstore"
	"norn/v2/api/hub"
	"norn/v2/api/store"
)

// ControlStore is the union of every backend-neutral control boundary. Both
// *store.DB and the etcd composite satisfy it. The boundaries have no
// overlapping exported method names, so the union is unambiguous.
type ControlStore interface {
	store.OperationStore
	store.DeploymentStore
	store.FleetAttemptStore
	store.MutationAuditStore
	hub.EventStore
	store.AuthStore
	store.NotificationStore
	store.WebhookStore
	store.CronStore
	store.FuncExecutionStore
	store.RecoveryDrillStore
	store.AccessPatternStore
	store.FleetGitHubDispatchStore
	store.BeaconStore
	store.DatabaseRecoveryInspector
}

const (
	BackendPostgres = "postgres"
	BackendEtcd     = "etcd"
)

// Config selects the control backend.
type Config struct {
	Backend       string   // "postgres" (default) or "etcd"
	EtcdEndpoints []string // required for the etcd backend
	EtcdPrefix    string   // key prefix for the etcd backend
}

// ConfigFromEnv reads the control-backend selection from the environment.
// NORN_CONTROL_BACKEND selects the backend (default "postgres");
// NORN_ETCD_ENDPOINTS (comma-separated) and NORN_ETCD_PREFIX configure etcd.
func ConfigFromEnv() Config {
	backend := strings.TrimSpace(os.Getenv("NORN_CONTROL_BACKEND"))
	if backend == "" {
		backend = BackendPostgres
	}
	prefix := strings.TrimSpace(os.Getenv("NORN_ETCD_PREFIX"))
	if prefix == "" {
		prefix = "/norn"
	}
	var endpoints []string
	if raw := strings.TrimSpace(os.Getenv("NORN_ETCD_ENDPOINTS")); raw != "" {
		for _, e := range strings.Split(raw, ",") {
			if e = strings.TrimSpace(e); e != "" {
				endpoints = append(endpoints, e)
			}
		}
	}
	return Config{Backend: backend, EtcdEndpoints: endpoints, EtcdPrefix: prefix}
}

// etcdControlStore composes the etcd adapters into one ControlStore. The
// boundaries have no overlapping exported method names, so promotion is
// unambiguous.
type etcdControlStore struct {
	*etcdstore.OperationStore
	*etcdstore.DeploymentStore
	*etcdstore.FleetAttemptStore
	*etcdstore.MutationAuditStore
	*etcdstore.EventStore
	*etcdstore.AuthStore
	*etcdstore.NotificationStore
	*etcdstore.WebhookStore
	*etcdstore.CronStore
	*etcdstore.FuncExecutionStore
	*etcdstore.RecoveryDrillStore
	*etcdstore.AccessPatternStore
	*etcdstore.FleetGitHubDispatchStore
	*etcdstore.BeaconStore
	*etcdstore.RecoveryInspector
}

var (
	_ ControlStore = (*store.DB)(nil)
	_ ControlStore = (*etcdControlStore)(nil)
)

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// New returns the control store for cfg. For the PostgreSQL backend it reuses
// the already-connected *store.DB (pgDB) and a no-op closer. For the etcd
// backend it dials the cluster and returns a closer that shuts the client down;
// pgDB may be nil in that case.
func New(pgDB *store.DB, cfg Config) (ControlStore, io.Closer, error) {
	switch cfg.Backend {
	case BackendPostgres, "":
		if pgDB == nil {
			return nil, nil, fmt.Errorf("controlstore: postgres backend requires a connected *store.DB")
		}
		return pgDB, noopCloser{}, nil
	case BackendEtcd:
		if len(cfg.EtcdEndpoints) == 0 {
			return nil, nil, fmt.Errorf("controlstore: etcd backend requires NORN_ETCD_ENDPOINTS")
		}
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   cfg.EtcdEndpoints,
			DialTimeout: 5 * time.Second,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("controlstore: dial etcd: %w", err)
		}
		cs := &etcdControlStore{
			OperationStore:           etcdstore.NewOperationStore(cli, cfg.EtcdPrefix),
			DeploymentStore:          etcdstore.NewDeploymentStore(cli, cfg.EtcdPrefix),
			FleetAttemptStore:        etcdstore.NewFleetAttemptStore(cli, cfg.EtcdPrefix),
			MutationAuditStore:       etcdstore.NewMutationAuditStore(cli, cfg.EtcdPrefix),
			EventStore:               etcdstore.NewEventStore(cli, cfg.EtcdPrefix),
			AuthStore:                etcdstore.NewAuthStore(cli, cfg.EtcdPrefix),
			NotificationStore:        etcdstore.NewNotificationStore(cli, cfg.EtcdPrefix),
			WebhookStore:             etcdstore.NewWebhookStore(cli, cfg.EtcdPrefix),
			CronStore:                etcdstore.NewCronStore(cli, cfg.EtcdPrefix),
			FuncExecutionStore:       etcdstore.NewFuncExecutionStore(cli, cfg.EtcdPrefix),
			RecoveryDrillStore:       etcdstore.NewRecoveryDrillStore(cli, cfg.EtcdPrefix),
			AccessPatternStore:       etcdstore.NewAccessPatternStore(cli, cfg.EtcdPrefix),
			FleetGitHubDispatchStore: etcdstore.NewFleetGitHubDispatchStore(cli, cfg.EtcdPrefix),
			BeaconStore:              etcdstore.NewBeaconStore(cli, cfg.EtcdPrefix),
			RecoveryInspector:        etcdstore.NewRecoveryInspector(cli),
		}
		return cs, cli, nil
	default:
		return nil, nil, fmt.Errorf("controlstore: unknown backend %q", cfg.Backend)
	}
}
