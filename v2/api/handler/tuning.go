package handler

import (
	"math"
	"net/http"
	"sort"
	"time"

	"norn/v2/api/engine"
	"norn/v2/api/model"
)

type tuningRecommendation struct {
	App         string               `json:"app"`
	Process     string               `json:"process"`
	Mode        string               `json:"mode"`
	Confidence  string               `json:"confidence"`
	Current     tuningResourceState  `json:"current"`
	Recommended tuningResourceState  `json:"recommended"`
	Observed    tuningObserved       `json:"observed"`
	Signals     []tuningSignalResult `json:"signals"`
	Actions     []string             `json:"actions"`
	Reasons     []string             `json:"reasons"`
}

type tuningResourceState struct {
	CPU    int `json:"cpuMHz"`
	Memory int `json:"memoryMB"`
	Scale  int `json:"scale"`
}

type tuningObserved struct {
	UsedMemoryMB      int        `json:"usedMemoryMB"`
	PeakMemoryMB      int        `json:"peakMemoryMB"`
	MemoryUtilization float64    `json:"memoryUtilization"`
	CPUPercent        float64    `json:"cpuPercent"`
	AllocationCount   int        `json:"allocationCount"`
	Source            string     `json:"source"`
	AccessRequests    int64      `json:"accessRequests,omitempty"`
	LastAccessAt      *time.Time `json:"lastAccessAt,omitempty"`
	QuietForHours     *float64   `json:"quietForHours,omitempty"`
	IdleCandidate     bool       `json:"idleCandidate,omitempty"`
}

type tuningSignalResult struct {
	Name      string  `json:"name"`
	Source    string  `json:"source"`
	Metric    string  `json:"metric"`
	Window    string  `json:"window,omitempty"`
	Aggregate string  `json:"aggregate,omitempty"`
	Value     float64 `json:"value,omitempty"`
	Unit      string  `json:"unit,omitempty"`
	Available bool    `json:"available"`
	Reason    string  `json:"reason,omitempty"`
}

type tuningUsage struct {
	UsedMemoryMB int
	PeakMemoryMB int
	CPUPercent   float64
	Allocations  int
}

func (h *Handler) TuningRecommendations(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not available")
		return
	}

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].App < specs[j].App })

	var recommendations []tuningRecommendation
	accessByGroup := map[string]accessPatternSummary{}
	if h.db != nil {
		if patterns, err := h.buildAccessPatternSummaries(r.Context(), defaultAccessPatternWindow, defaultIdleCandidateAfter); err == nil {
			for _, pattern := range patterns {
				accessByGroup[accessPatternKey(pattern.App, pattern.Process)] = pattern
			}
		}
	}
	for _, spec := range specs {
		usage, err := h.engine.JobResourceUsage(r.Context(), spec.App)
		if err != nil || len(usage) == 0 {
			continue
		}
		usageByGroup := aggregateTuningUsage(usage)
		for procName, proc := range spec.Processes {
			u, ok := usageByGroup[procName]
			if !ok {
				continue
			}
			rec := buildTuningRecommendation(spec.App, procName, proc, u)
			if pattern, ok := accessByGroup[accessPatternKey(spec.App, procName)]; ok {
				enrichTuningWithAccess(&rec, pattern)
			}
			recommendations = append(recommendations, rec)
		}
	}

	writeJSON(w, map[string]interface{}{
		"recommendations": recommendations,
	})
}

func enrichTuningWithAccess(rec *tuningRecommendation, pattern accessPatternSummary) {
	rec.Observed.AccessRequests = pattern.TotalRequests
	rec.Observed.LastAccessAt = pattern.LastSeen
	rec.Observed.QuietForHours = pattern.QuietForHours
	rec.Observed.IdleCandidate = pattern.IdleCandidate
	rec.Signals = append(rec.Signals, tuningSignalResult{
		Name:      "access_requests",
		Source:    "norn",
		Metric:    "access_requests",
		Window:    "14d",
		Aggregate: "sum",
		Value:     float64(pattern.TotalRequests),
		Unit:      "requests",
		Available: true,
	})
	if pattern.QuietForHours != nil {
		rec.Signals = append(rec.Signals, tuningSignalResult{
			Name:      "quiet_for",
			Source:    "norn",
			Metric:    "quiet_for_hours",
			Window:    "14d",
			Aggregate: "current",
			Value:     *pattern.QuietForHours,
			Unit:      "hours",
			Available: true,
		})
	}
	if !pattern.IdleCandidate {
		return
	}
	action := "candidate_idle"
	if pattern.RecommendedAction == "observe_before_idle" {
		action = "observe_access"
	}
	rec.Actions = append(rec.Actions, action)
	if pattern.IdleReason != "" {
		rec.Reasons = append(rec.Reasons, pattern.IdleReason)
	}
	if rec.Confidence == "medium" && pattern.Confidence == "low" {
		rec.Confidence = "low"
	}
}

func aggregateTuningUsage(usage []engine.ResourceUsage) map[string]tuningUsage {
	usageByGroup := map[string]tuningUsage{}
	for _, u := range usage {
		current := usageByGroup[u.TaskGroup]
		usedMB := int(u.MemoryUsageBytes / (1024 * 1024))
		peakMB := int(u.MemoryMaxBytes / (1024 * 1024))
		if usedMB > current.UsedMemoryMB {
			current.UsedMemoryMB = usedMB
		}
		if peakMB > current.PeakMemoryMB {
			current.PeakMemoryMB = peakMB
		}
		if u.CPUPercent > current.CPUPercent {
			current.CPUPercent = u.CPUPercent
		}
		current.Allocations++
		usageByGroup[u.TaskGroup] = current
	}
	return usageByGroup
}

func buildTuningRecommendation(app, process string, proc model.Process, usage tuningUsage) tuningRecommendation {
	current := tuningResourceState{CPU: 100, Memory: 128, Scale: 1}
	if proc.Resources != nil {
		if proc.Resources.CPU > 0 {
			current.CPU = proc.Resources.CPU
		}
		if proc.Resources.Memory > 0 {
			current.Memory = proc.Resources.Memory
		}
	}
	if usage.Allocations > 0 {
		current.Scale = usage.Allocations
	} else if proc.Scaling != nil && proc.Scaling.Min > 0 {
		current.Scale = proc.Scaling.Min
	}

	mode := "advisory"
	if proc.Tuning != nil && proc.Tuning.Mode != "" {
		mode = proc.Tuning.Mode
	}

	highMem := usage.PeakMemoryMB
	if highMem == 0 {
		highMem = usage.UsedMemoryMB
	}
	memUtil := 0.0
	if current.Memory > 0 && highMem > 0 {
		memUtil = float64(highMem) / float64(current.Memory)
	}

	rec := tuningRecommendation{
		App:        app,
		Process:    process,
		Mode:       mode,
		Confidence: "medium",
		Current:    current,
		Recommended: tuningResourceState{
			CPU:    current.CPU,
			Memory: current.Memory,
			Scale:  current.Scale,
		},
		Observed: tuningObserved{
			UsedMemoryMB:      usage.UsedMemoryMB,
			PeakMemoryMB:      usage.PeakMemoryMB,
			MemoryUtilization: memUtil,
			CPUPercent:        usage.CPUPercent,
			AllocationCount:   usage.Allocations,
			Source:            "engine.live",
		},
	}

	if usage.PeakMemoryMB == 0 {
		rec.Confidence = "low"
		rec.Reasons = append(rec.Reasons, "only live memory usage is available; no peak signal reported")
	}
	if proc.Tuning != nil && len(proc.Tuning.Signals) > 0 {
		rec.Signals = tuningSignalsFromPolicy(proc.Tuning.Signals, usage)
	} else {
		rec.Signals = defaultTuningSignals(usage)
	}

	recommendMemory(&rec, proc.Tuning, highMem, memUtil)
	recommendCPU(&rec, proc.Tuning, usage.CPUPercent)
	recommendScale(&rec, proc, usage.CPUPercent, memUtil)
	if len(rec.Actions) == 0 {
		rec.Actions = append(rec.Actions, "keep")
		rec.Reasons = append(rec.Reasons, "observed usage is inside advisory thresholds")
	}
	return rec
}

func recommendMemory(rec *tuningRecommendation, tuning *model.TuningPolicy, highMem int, memUtil float64) {
	if rec.Current.Memory == 0 || highMem == 0 {
		return
	}
	target := rec.Current.Memory
	switch {
	case memUtil > 0.80:
		target = roundUpMB(maxInt(int(math.Ceil(float64(highMem)*1.5)), int(math.Ceil(float64(rec.Current.Memory)*1.5))))
		target = clampMemory(tuning, target)
		if target > rec.Current.Memory {
			rec.Recommended.Memory = target
			rec.Actions = append(rec.Actions, "increase_memory")
			rec.Reasons = append(rec.Reasons, "memory signal exceeds 80% of declared limit")
		}
	case memUtil < 0.30:
		target = maxInt(rec.Current.Memory/2, highMem*2)
		target = roundUpMB(target)
		target = clampMemory(tuning, target)
		if target > 0 && target < rec.Current.Memory {
			rec.Recommended.Memory = target
			rec.Actions = append(rec.Actions, "decrease_memory")
			rec.Reasons = append(rec.Reasons, "memory signal is below 30% of declared limit")
		}
	}
}

func recommendCPU(rec *tuningRecommendation, tuning *model.TuningPolicy, cpuPercent float64) {
	if rec.Current.CPU == 0 {
		return
	}
	target := rec.Current.CPU
	switch {
	case cpuPercent > 80:
		target = roundUpCPU(maxInt(int(math.Ceil(float64(rec.Current.CPU)*1.5)), rec.Current.CPU+25))
		target = clampCPU(tuning, target)
		if target > rec.Current.CPU {
			rec.Recommended.CPU = target
			rec.Actions = append(rec.Actions, "increase_cpu")
			rec.Reasons = append(rec.Reasons, "cpu signal exceeds 80%")
		}
	case cpuPercent < 10 && rec.Current.CPU > 25:
		target = roundUpCPU(maxInt(rec.Current.CPU/2, 25))
		target = clampCPU(tuning, target)
		if target > 0 && target < rec.Current.CPU {
			rec.Recommended.CPU = target
			rec.Actions = append(rec.Actions, "decrease_cpu")
			rec.Reasons = append(rec.Reasons, "cpu signal is below 10%")
		}
	}
}

func recommendScale(rec *tuningRecommendation, proc model.Process, cpuPercent, memUtil float64) {
	if proc.Scaling == nil || rec.Current.Scale == 0 {
		return
	}
	minScale := proc.Scaling.Min
	if minScale == 0 {
		minScale = 1
	}
	maxScale := proc.Scaling.Max
	switch {
	case (cpuPercent > 70 || memUtil > 0.80) && maxScale > 0 && rec.Current.Scale < maxScale:
		rec.Recommended.Scale = rec.Current.Scale + 1
		rec.Actions = append(rec.Actions, "increase_scale")
		rec.Reasons = append(rec.Reasons, "load signal is above scale-up threshold")
	case cpuPercent < 10 && memUtil < 0.30 && rec.Current.Scale > minScale:
		rec.Recommended.Scale = rec.Current.Scale - 1
		rec.Actions = append(rec.Actions, "decrease_scale")
		rec.Reasons = append(rec.Reasons, "load signal is below scale-down threshold")
	}
}

func defaultTuningSignals(usage tuningUsage) []tuningSignalResult {
	return []tuningSignalResult{
		{Name: "memory_rss", Source: "engine", Metric: "memory_rss", Aggregate: "current", Value: float64(usage.UsedMemoryMB), Unit: "MB", Available: usage.UsedMemoryMB > 0},
		{Name: "memory_max", Source: "engine", Metric: "memory_max", Aggregate: "max", Value: float64(usage.PeakMemoryMB), Unit: "MB", Available: usage.PeakMemoryMB > 0, Reason: unavailableReason(usage.PeakMemoryMB > 0, "no peak memory value reported")},
		{Name: "cpu_percent", Source: "engine", Metric: "cpu_percent", Aggregate: "current", Value: usage.CPUPercent, Unit: "percent", Available: true},
	}
}

func tuningSignalsFromPolicy(signals []model.TuningSignal, usage tuningUsage) []tuningSignalResult {
	out := make([]tuningSignalResult, 0, len(signals))
	for _, signal := range signals {
		result := tuningSignalResult{
			Name:      firstNonEmptyString(signal.Name, signal.Metric),
			Source:    firstNonEmptyString(signal.Source, "engine"),
			Metric:    signal.Metric,
			Window:    signal.Window,
			Aggregate: signal.Aggregate,
		}
		switch result.Source + ":" + result.Metric {
		case "engine:memory_rss":
			result.Value = float64(usage.UsedMemoryMB)
			result.Unit = "MB"
			result.Available = usage.UsedMemoryMB > 0
			result.Reason = unavailableReason(result.Available, "no live memory value reported")
		case "engine:memory_max":
			result.Value = float64(usage.PeakMemoryMB)
			result.Unit = "MB"
			result.Available = usage.PeakMemoryMB > 0
			result.Reason = unavailableReason(result.Available, "no peak memory value reported")
		case "engine:cpu_percent":
			result.Value = usage.CPUPercent
			result.Unit = "percent"
			result.Available = true
		default:
			result.Available = false
			result.Reason = "signal source is declared but not connected to the advisory tuner yet"
		}
		out = append(out, result)
	}
	return out
}

func clampMemory(tuning *model.TuningPolicy, value int) int {
	if tuning == nil || tuning.Limits == nil {
		return maxInt(value, 128)
	}
	if tuning.Limits.Min.Memory > 0 && value < tuning.Limits.Min.Memory {
		value = tuning.Limits.Min.Memory
	}
	if tuning.Limits.Max.Memory > 0 && value > tuning.Limits.Max.Memory {
		value = tuning.Limits.Max.Memory
	}
	return value
}

func clampCPU(tuning *model.TuningPolicy, value int) int {
	if tuning == nil || tuning.Limits == nil {
		return maxInt(value, 25)
	}
	if tuning.Limits.Min.CPU > 0 && value < tuning.Limits.Min.CPU {
		value = tuning.Limits.Min.CPU
	}
	if tuning.Limits.Max.CPU > 0 && value > tuning.Limits.Max.CPU {
		value = tuning.Limits.Max.CPU
	}
	return value
}

func roundUpMB(value int) int {
	return roundUp(value, 64)
}

func roundUpCPU(value int) int {
	return roundUp(value, 25)
}

func roundUp(value, step int) int {
	if value <= 0 || step <= 0 {
		return value
	}
	return ((value + step - 1) / step) * step
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func unavailableReason(available bool, reason string) string {
	if available {
		return ""
	}
	return reason
}
