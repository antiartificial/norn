package handler

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestHostMetricsRequiresAPIReadScope(t *testing.T) {
	h := &Handler{hostMetrics: newHostMetricsCache(func(context.Context) (hostMetricsSample, error) {
		return hostMetricsSample{}, nil
	}, time.Now, time.Second)}
	rec := httptest.NewRecorder()
	h.HostMetrics(rec, httptest.NewRequest(http.MethodGet, "/api/v1/host/metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHostMetricsCachesSamplesAndReturnsCPU(t *testing.T) {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now := base
	calls := 0
	cache := newHostMetricsCache(func(context.Context) (hostMetricsSample, error) {
		calls++
		return hostMetricsSample{
			observedAt: now, utilizationPercent: float64(calls * 25), logicalCores: 8,
			memory: HostMemoryMetrics{TotalBytes: 1000, UsedBytes: 700, AvailableBytes: 300},
		}, nil
	}, func() time.Time { return now }, 5*time.Second)
	h := &Handler{hostMetrics: cache}

	get := func() HostMetricsResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/host/metrics", nil)
		req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopeAPIRead}})
		rec := httptest.NewRecorder()
		h.HostMetrics(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
		}
		var got HostMetricsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	first := get()
	if first.CPU.UtilizationPercent != 25 || first.SchemaVersion != "norn.host-metrics/v1" || first.Memory.UsedBytes != 700 {
		t.Fatalf("first response = %+v", first)
	}
	_ = get()
	if calls != 1 {
		t.Fatalf("sampler calls = %d, want 1 while cached", calls)
	}
	now = now.Add(5 * time.Second)
	second := get()
	if calls != 2 || second.CPU.UtilizationPercent != 50 || second.Stale {
		t.Fatalf("second response = %+v, calls = %d", second, calls)
	}
}

func TestHostMetricsReturnsLastSampleAsStaleAfterRefreshFailure(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	calls := 0
	cache := newHostMetricsCache(func(context.Context) (hostMetricsSample, error) {
		calls++
		if calls == 2 {
			return hostMetricsSample{}, errors.New("vm_stat unavailable")
		}
		return hostMetricsSample{observedAt: now, logicalCores: 4, memory: HostMemoryMetrics{TotalBytes: 1}}, nil
	}, func() time.Time { return now }, time.Second)
	if _, err := cache.snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	got, err := cache.snapshot(context.Background())
	if err != nil || !got.Stale || got.ObservedAt != time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) {
		t.Fatalf("response = %+v, err = %v", got, err)
	}
}

func TestParseVMStatUsesMacOSReclaimablePages(t *testing.T) {
	pageSize, pages, err := parseVMStat("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:                               2.\nPages active:                            3.\nPages inactive:                          5.\nPages speculative:                       7.\n")
	if err != nil || pageSize != 16384 || pages["Pages inactive"] != 5 || pages["Pages speculative"] != 7 {
		t.Fatalf("pageSize=%d pages=%v err=%v", pageSize, pages, err)
	}
}

func TestParseTopCPUUtilization(t *testing.T) {
	got, err := parseTopCPUUtilization("Processes: 842 total\nCPU usage: 16.24% user, 16.24% sys, 67.51% idle\n")
	if err != nil || math.Abs(got-32.49) > 0.001 {
		t.Fatalf("utilization=%v err=%v", got, err)
	}
}

func TestSampleDarwinHostMetrics(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS host sampler")
	}
	got, err := sampleDarwinHostMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.logicalCores < 1 || got.memory.TotalBytes == 0 || got.memory.TotalBytes != got.memory.UsedBytes+got.memory.AvailableBytes || got.utilizationPercent < 0 || got.utilizationPercent > 100 {
		t.Fatalf("sample = %+v", got)
	}
}
