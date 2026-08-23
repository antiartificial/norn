package handler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultHostMetricsSamplePeriod = 5 * time.Second

// HostMetricsResponse is machine-wide host telemetry, deliberately separate
// from Norn's Prometheus endpoint and app/container metrics.
type HostMetricsResponse struct {
	SchemaVersion       string            `json:"schemaVersion"`
	ObservedAt          time.Time         `json:"observedAt"`
	Stale               bool              `json:"stale"`
	SamplePeriodSeconds int               `json:"samplePeriodSeconds"`
	CPU                 HostCPUMetrics    `json:"cpu"`
	Memory              HostMemoryMetrics `json:"memory"`
}

type HostCPUMetrics struct {
	UtilizationPercent float64 `json:"utilizationPercent"`
	LogicalCores       int     `json:"logicalCores"`
}

type HostMemoryMetrics struct {
	TotalBytes     uint64 `json:"totalBytes"`
	UsedBytes      uint64 `json:"usedBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
}

type hostMetricsSample struct {
	observedAt         time.Time
	utilizationPercent float64
	logicalCores       int
	memory             HostMemoryMetrics
}

type hostMetricsSampler func(context.Context) (hostMetricsSample, error)

// hostMetricsCache serializes refreshes so simultaneous API clients neither
// stampede host commands nor observe a partially refreshed sample.
type hostMetricsCache struct {
	mu            sync.Mutex
	sampler       hostMetricsSampler
	now           func() time.Time
	period        time.Duration
	last          hostMetricsSample
	responseCache HostMetricsResponse
	have          bool
}

func newHostMetricsCache(sampler hostMetricsSampler, now func() time.Time, period time.Duration) *hostMetricsCache {
	if now == nil {
		now = time.Now
	}
	if period <= 0 {
		period = defaultHostMetricsSamplePeriod
	}
	return &hostMetricsCache{sampler: sampler, now: now, period: period}
}

func (c *hostMetricsCache) snapshot(ctx context.Context) (HostMetricsResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now().UTC()
	if c.have && now.Sub(c.last.observedAt) < c.period {
		return c.responseCache, nil
	}
	if c.sampler == nil {
		return HostMetricsResponse{}, errors.New("host metrics sampler is unavailable")
	}

	next, err := c.sampler(ctx)
	if err != nil {
		if c.have {
			stale := c.responseCache
			stale.Stale = true
			return stale, nil
		}
		return HostMetricsResponse{}, err
	}
	if next.observedAt.IsZero() {
		next.observedAt = now
	}
	next.observedAt = next.observedAt.UTC()
	if next.logicalCores < 1 {
		next.logicalCores = 1
	}
	c.last, c.have = next, true
	c.responseCache = c.response(next)
	return c.responseCache, nil
}

func (c *hostMetricsCache) response(sample hostMetricsSample) HostMetricsResponse {
	utilization := math.Max(0, math.Min(100, sample.utilizationPercent))
	return HostMetricsResponse{
		SchemaVersion:       "norn.host-metrics/v1",
		ObservedAt:          sample.observedAt,
		Stale:               false,
		SamplePeriodSeconds: int(c.period.Seconds()),
		CPU: HostCPUMetrics{
			UtilizationPercent: utilization,
			LogicalCores:       sample.logicalCores,
		},
		Memory: sample.memory,
	}
}

// HostMetrics serves a cached machine-wide CPU and memory sample. The route
// requires an explicit principal even when legacy loopback auth is enabled.
func (h *Handler) HostMetrics(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	if h.hostMetrics == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "host_metrics_unavailable", "host metrics collection is unavailable")
		return
	}
	out, err := h.hostMetrics.snapshot(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "host_metrics_unavailable", "host metrics collection failed")
		return
	}
	writeJSON(w, out)
}

// defaultHostMetricsSampler uses macOS counters for the complete host. On
// macOS, memory "available" is free plus reclaimable inactive/speculative
// pages; used is the remainder of physical memory, so they always partition
// total memory. CPU is top's machine-wide non-idle percentage at collection.
func defaultHostMetricsSampler(ctx context.Context) (hostMetricsSample, error) {
	if runtime.GOOS != "darwin" {
		return hostMetricsSample{}, fmt.Errorf("host metrics collection is only implemented for macOS")
	}
	return sampleDarwinHostMetrics(ctx)
}

func sampleDarwinHostMetrics(ctx context.Context) (hostMetricsSample, error) {
	topText, err := runHostMetricsCommand(ctx, "/usr/bin/top", "-l", "1", "-n", "0")
	if err != nil {
		return hostMetricsSample{}, fmt.Errorf("read CPU utilization: %w", err)
	}
	utilization, err := parseTopCPUUtilization(topText)
	if err != nil {
		return hostMetricsSample{}, err
	}

	coresText, err := runHostMetricsCommand(ctx, "/usr/sbin/sysctl", "-n", "hw.logicalcpu")
	if err != nil {
		return hostMetricsSample{}, fmt.Errorf("read hw.logicalcpu: %w", err)
	}
	cores, err := strconv.Atoi(strings.TrimSpace(coresText))
	if err != nil || cores < 1 {
		return hostMetricsSample{}, fmt.Errorf("parse hw.logicalcpu %q", strings.TrimSpace(coresText))
	}

	totalText, err := runHostMetricsCommand(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil {
		return hostMetricsSample{}, fmt.Errorf("read hw.memsize: %w", err)
	}
	total, err := strconv.ParseUint(strings.TrimSpace(totalText), 10, 64)
	if err != nil || total == 0 {
		return hostMetricsSample{}, fmt.Errorf("parse hw.memsize %q", strings.TrimSpace(totalText))
	}

	vmStat, err := runHostMetricsCommand(ctx, "/usr/bin/vm_stat")
	if err != nil {
		return hostMetricsSample{}, fmt.Errorf("read vm_stat: %w", err)
	}
	pageSize, pages, err := parseVMStat(vmStat)
	if err != nil {
		return hostMetricsSample{}, err
	}
	availablePages := pages["Pages free"] + pages["Pages inactive"] + pages["Pages speculative"]
	available := availablePages * pageSize
	if available > total {
		available = total
	}

	return hostMetricsSample{
		observedAt: time.Now().UTC(), utilizationPercent: utilization, logicalCores: cores,
		memory: HostMemoryMetrics{TotalBytes: total, UsedBytes: total - available, AvailableBytes: available},
	}, nil
}

var idleCPUPercentRE = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)%\s+idle`)

func parseTopCPUUtilization(value string) (float64, error) {
	for _, line := range strings.Split(value, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "CPU usage:") {
			continue
		}
		match := idleCPUPercentRE.FindStringSubmatch(line)
		if len(match) != 2 {
			break
		}
		idle, err := strconv.ParseFloat(match[1], 64)
		if err != nil || idle < 0 || idle > 100 {
			break
		}
		return math.Max(0, math.Min(100, 100-idle)), nil
	}
	return 0, errors.New("parse macOS top CPU usage")
}

func runHostMetricsCommand(ctx context.Context, name string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(commandCtx, name, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func parseVMStat(value string) (uint64, map[string]uint64, error) {
	lines := strings.Split(value, "\n")
	if len(lines) == 0 {
		return 0, nil, errors.New("empty vm_stat output")
	}
	pageSize := uint64(4096)
	if start := strings.Index(lines[0], "page size of "); start >= 0 {
		remainder := lines[0][start+len("page size of "):]
		end := strings.Index(remainder, " bytes")
		if end < 0 {
			return 0, nil, fmt.Errorf("parse vm_stat page size %q", lines[0])
		}
		parsed, err := strconv.ParseUint(strings.TrimSpace(remainder[:end]), 10, 64)
		if err != nil || parsed == 0 {
			return 0, nil, fmt.Errorf("parse vm_stat page size %q", lines[0])
		}
		pageSize = parsed
	}
	pages := make(map[string]uint64)
	for _, line := range lines[1:] {
		keyEnd := strings.Index(line, ":")
		if keyEnd < 0 {
			continue
		}
		key := strings.TrimSpace(line[:keyEnd])
		raw := strings.Trim(strings.TrimSpace(line[keyEnd+1:]), ".")
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err == nil {
			pages[key] = parsed
		}
	}
	if _, ok := pages["Pages free"]; !ok {
		return 0, nil, errors.New("vm_stat did not report free pages")
	}
	return pageSize, pages, nil
}
