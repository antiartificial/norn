package handler

import (
	"testing"

	"norn/v2/api/model"
)

func TestBuildTuningRecommendationSuggestsHalfDownMemoryAndCPU(t *testing.T) {
	rec := buildTuningRecommendation("quiet-app", "web", model.Process{
		Resources: &model.Resources{CPU: 100, Memory: 1024},
	}, tuningUsage{
		UsedMemoryMB: 80,
		PeakMemoryMB: 100,
		CPUPercent:   2,
		Allocations:  1,
	})

	if rec.Recommended.Memory != 512 {
		t.Fatalf("recommended memory = %d, want 512", rec.Recommended.Memory)
	}
	if rec.Recommended.CPU != 50 {
		t.Fatalf("recommended cpu = %d, want 50", rec.Recommended.CPU)
	}
	assertAction(t, rec.Actions, "decrease_memory")
	assertAction(t, rec.Actions, "decrease_cpu")
}

func TestBuildTuningRecommendationClampsToTuningLimits(t *testing.T) {
	rec := buildTuningRecommendation("limited-app", "web", model.Process{
		Resources: &model.Resources{CPU: 100, Memory: 1024},
		Tuning: &model.TuningPolicy{
			Mode: "advisory",
			Limits: &model.TuningLimits{
				Min: model.TuningProfile{CPU: 75, Memory: 768, Scale: 1},
				Max: model.TuningProfile{CPU: 200, Memory: 2048, Scale: 2},
			},
		},
	}, tuningUsage{
		UsedMemoryMB: 40,
		PeakMemoryMB: 80,
		CPUPercent:   1,
		Allocations:  1,
	})

	if rec.Recommended.Memory != 768 {
		t.Fatalf("recommended memory = %d, want min clamp 768", rec.Recommended.Memory)
	}
	if rec.Recommended.CPU != 75 {
		t.Fatalf("recommended cpu = %d, want min clamp 75", rec.Recommended.CPU)
	}
}

func TestBuildTuningRecommendationReportsDeclaredSignals(t *testing.T) {
	rec := buildTuningRecommendation("signals-app", "web", model.Process{
		Resources: &model.Resources{CPU: 50, Memory: 512},
		Tuning: &model.TuningPolicy{
			Signals: []model.TuningSignal{
				{Name: "rss", Source: "nomad", Metric: "memory_rss"},
				{Name: "p95", Source: "prometheus", Metric: "container_memory_working_set_bytes", Window: "24h", Aggregate: "p95"},
			},
		},
	}, tuningUsage{
		UsedMemoryMB: 120,
		CPUPercent:   4,
		Allocations:  1,
	})

	if len(rec.Signals) != 2 {
		t.Fatalf("signals = %+v, want 2", rec.Signals)
	}
	if !rec.Signals[0].Available || rec.Signals[0].Value != 120 {
		t.Fatalf("nomad signal = %+v, want available value 120", rec.Signals[0])
	}
	if rec.Signals[1].Available || rec.Signals[1].Reason == "" {
		t.Fatalf("prometheus signal = %+v, want unavailable with reason", rec.Signals[1])
	}
	if rec.Confidence != "low" {
		t.Fatalf("confidence = %q, want low without peak signal", rec.Confidence)
	}
}

func assertAction(t *testing.T, actions []string, want string) {
	t.Helper()
	for _, action := range actions {
		if action == want {
			return
		}
	}
	t.Fatalf("actions = %+v, missing %q", actions, want)
}
