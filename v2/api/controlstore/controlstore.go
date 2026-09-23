// Package controlstore selects and constructs the backend for Norn's core
// control-plane boundaries (operations, deployments, events, Fleet attempts and
// evidence). The same ControlStore contract is satisfied by the PostgreSQL
// adapter (*store.DB) and by the etcd adapters (etcdstore), so the control plane
// can be pointed at either — the mechanism v3 needs for a fresh HA Fleet on etcd
// with no control PostgreSQL.
//
// NOTE: this selects only the five control boundaries. The rest of Norn's store
// surface (devices/tokens, webhooks, beacon, access grants, exec sessions, etc.)
// is not yet abstracted, so the etcd backend is control-boundary-only and
// experimental until those land; the PostgreSQL default is unchanged.
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

// ControlStore is the union of the five backend-neutral control boundaries.
// Both *store.DB and the etcd composite satisfy it.
type ControlStore interface {
	store.OperationStore
	store.DeploymentStore
	store.FleetAttemptStore
	store.MutationAuditStore
	hub.EventStore
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

// etcdControlStore composes the five etcd adapters into one ControlStore. The
// boundaries have no overlapping method names, so promotion is unambiguous.
type etcdControlStore struct {
	*etcdstore.OperationStore
	*etcdstore.DeploymentStore
	*etcdstore.FleetAttemptStore
	*etcdstore.MutationAuditStore
	*etcdstore.EventStore
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
			OperationStore:     etcdstore.NewOperationStore(cli, cfg.EtcdPrefix),
			DeploymentStore:    etcdstore.NewDeploymentStore(cli, cfg.EtcdPrefix),
			FleetAttemptStore:  etcdstore.NewFleetAttemptStore(cli, cfg.EtcdPrefix),
			MutationAuditStore: etcdstore.NewMutationAuditStore(cli, cfg.EtcdPrefix),
			EventStore:         etcdstore.NewEventStore(cli, cfg.EtcdPrefix),
		}
		return cs, cli, nil
	default:
		return nil, nil, fmt.Errorf("controlstore: unknown backend %q", cfg.Backend)
	}
}
