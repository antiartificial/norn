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
	op  clientv3.Op
}

func (s *etcdHealthStub) Get(_ context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	s.op = clientv3.OpGet(key, opts...)
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
	if got := string(client.op.KeyBytes()); got != "/norn/source-validation/" {
		t.Fatalf("read key=%q", got)
	}
	if got, want := string(client.op.RangeBytes()), clientv3.GetPrefixRangeEnd("/norn/source-validation/"); got != want {
		t.Fatalf("read range end=%q, want %q", got, want)
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

func TestEtcdPassiveHealthTracksQuorumAfterStartup(t *testing.T) {
	client := &etcdHealthStub{}
	health := etcdPassiveHealthHandler(client, "/norn/passive")
	schema := etcdPassiveSchemaHandler(client, "/norn/passive")
	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	ready := httptest.NewRecorder()
	health.ServeHTTP(ready, request)
	if ready.Code != http.StatusOK || string(client.op.KeyBytes()) != "/norn/passive/" {
		t.Fatalf("initial passive health: status=%d read=%q", ready.Code, client.op.KeyBytes())
	}
	schemaReady := httptest.NewRecorder()
	schema.ServeHTTP(schemaReady, httptest.NewRequest(http.MethodGet, "/api/schema", nil))
	if schemaReady.Code != http.StatusOK {
		t.Fatalf("initial passive schema status=%d", schemaReady.Code)
	}
	client.err = errors.New("quorum unavailable")
	unavailable := httptest.NewRecorder()
	health.ServeHTTP(unavailable, request)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("passive candidate stayed healthy after quorum loss: status=%d", unavailable.Code)
	}
	schemaUnavailable := httptest.NewRecorder()
	schema.ServeHTTP(schemaUnavailable, httptest.NewRequest(http.MethodGet, "/api/schema", nil))
	if schemaUnavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("passive schema check stayed successful after quorum loss: status=%d", schemaUnavailable.Code)
	}
}
