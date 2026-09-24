package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type etcdHealthStub struct {
	err error
	key string
}

func (s *etcdHealthStub) Get(_ context.Context, key string, _ ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	s.key = key
	if s.err != nil {
		return nil, s.err
	}
	return &clientv3.GetResponse{}, nil
}

func TestCheckEtcdSourceHealthUsesControlPrefixRead(t *testing.T) {
	client := &etcdHealthStub{}
	err := checkEtcdSourceHealth(context.Background(), client, "/norn/source-validation")
	if err != nil {
		t.Fatalf("linearizable read: %v", err)
	}
	if client.key != "/norn/source-validation" {
		t.Fatalf("read key=%q", client.key)
	}
}

func TestCheckEtcdSourceHealthFailsWhenQuorumReadFails(t *testing.T) {
	err := checkEtcdSourceHealth(context.Background(), &etcdHealthStub{err: errors.New("quorum unavailable")}, "/norn/source-validation")
	if err == nil {
		t.Fatal("unavailable quorum read must fail health")
	}
}

func TestSourceHealthUnavailableProblemShape(t *testing.T) {
	handler := sourceValidationHealthHandler(&etcdHealthStub{err: errors.New("down")}, "/norn/source-validation")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", recorder.Code)
	}
}
