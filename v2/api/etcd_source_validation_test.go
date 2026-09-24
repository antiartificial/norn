package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type etcdStatusStub struct {
	errs map[string]error
}

func (s etcdStatusStub) Status(_ context.Context, endpoint string) (*clientv3.StatusResponse, error) {
	if err := s.errs[endpoint]; err != nil {
		return nil, err
	}
	return &clientv3.StatusResponse{}, nil
}

func TestCheckEtcdSourceHealthAcceptsAnyConfiguredMember(t *testing.T) {
	err := checkEtcdSourceHealth(context.Background(), etcdStatusStub{errs: map[string]error{
		"http://first": errors.New("unavailable"),
	}}, []string{"http://first", "http://second"})
	if err != nil {
		t.Fatalf("healthy second member: %v", err)
	}
}

func TestCheckEtcdSourceHealthFailsWhenEveryMemberIsUnavailable(t *testing.T) {
	err := checkEtcdSourceHealth(context.Background(), etcdStatusStub{errs: map[string]error{
		"http://first":  errors.New("first unavailable"),
		"http://second": errors.New("second unavailable"),
	}}, []string{"http://first", "http://second"})
	if err == nil {
		t.Fatal("all unavailable members must fail health")
	}
}

func TestSourceHealthUnavailableProblemShape(t *testing.T) {
	handler := sourceValidationHealthHandler(etcdStatusStub{errs: map[string]error{"http://only": errors.New("down")}}, []string{"http://only"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", recorder.Code)
	}
}
