package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestRunObservesStoredSpecAndNeverClaimsPromotion(t *testing.T) {
	api := appSpecServer(t, false)
	defer api.Close()
	nomad := inventoryNomadServer(t)
	defer nomad.Close()
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--app", "fixture", "--api-url", api.URL, "--nomad-url", nomad.URL}, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		App              string `json:"app"`
		StoredSpecSHA256 string `json:"storedSpecSha256"`
		Regions          []struct {
			Jobs []struct {
				ID string `json:"id"`
			} `json:"jobs"`
		} `json:"regions"`
		ExternalWritersUnverified bool `json:"externalWritersUnverified"`
		PromotionReady            bool `json:"promotionReady"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.App != "fixture" || len(result.StoredSpecSHA256) != 64 || len(result.Regions) != 1 || len(result.Regions[0].Jobs) != 1 || result.Regions[0].Jobs[0].ID != "fixture" || !result.ExternalWritersUnverified || result.PromotionReady {
		t.Fatalf("output=%s", output.String())
	}
}

func TestRunRejectsStoredSpecDriftAndNonLoopback(t *testing.T) {
	api := appSpecServer(t, true)
	defer api.Close()
	nomad := inventoryNomadServer(t)
	defer nomad.Close()
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--app", "fixture", "--api-url", api.URL, "--nomad-url", nomad.URL}, &output); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("drift err=%v", err)
	}
	if err := run(context.Background(), []string{"--app", "fixture", "--api-url", "http://example.com:8800", "--nomad-url", nomad.URL}, &output); err == nil {
		t.Fatal("remote API accepted")
	}
}

func appSpecServer(t *testing.T, change bool) *httptest.Server {
	t.Helper()
	reads := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/apps/fixture" {
			http.NotFound(w, r)
			return
		}
		reads++
		process := "web"
		if change && reads == 2 {
			process = "worker"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"spec": map[string]any{"name": "fixture", "infrastructure": map[string]any{"postgres": map[string]any{}}, "processes": map[string]any{process: map[string]any{}}}})
	}))
}

func inventoryNomadServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("region") != "global" || r.URL.Query().Get("prefix") != "fixture" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/v1/jobs":
			_ = json.NewEncoder(w).Encode([]*nomadapi.JobListStub{{ID: "fixture", JobModifyIndex: 7, Status: "running"}})
		case "/v1/job/fixture":
			id, region, version, index, stopped := "fixture", "global", uint64(1), uint64(7), false
			_ = json.NewEncoder(w).Encode(&nomadapi.Job{ID: &id, Region: &region, Version: &version, JobModifyIndex: &index, Stop: &stopped})
		case "/v1/job/fixture/allocations":
			_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{})
		default:
			http.NotFound(w, r)
		}
	}))
}
