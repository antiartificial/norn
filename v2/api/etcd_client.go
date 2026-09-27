package main

import (
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/startup"
)

func newEtcdClient(backend startup.ControlBackendConfig) (*clientv3.Client, error) {
	tlsConfig, err := backend.EtcdTLSConfig()
	if err != nil {
		return nil, fmt.Errorf("etcd transport: %w", err)
	}
	return clientv3.New(clientv3.Config{
		Endpoints:   backend.EtcdEndpoints,
		DialTimeout: 5 * time.Second,
		TLS:         tlsConfig,
		Username:    backend.EtcdUsername,
		Password:    backend.EtcdPassword,
	})
}
