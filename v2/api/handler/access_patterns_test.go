package handler

import (
	"testing"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestSummarizeAccessPatternsFlagsUnobservedServices(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	summaries := summarizeAccessPatterns([]model.ServiceManifestEntry{
		{
			App:     "quiet-frontend",
			Process: "web",
			Type:    "service",
			Status:  "passing",
			Endpoints: []model.Endpoint{
				{URL: "https://quiet.example.test"},
			},
		},
	}, nil, 14*24*time.Hour, 7*24*time.Hour, now)

	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}
	got := summaries[0]
	if !got.IdleCandidate {
		t.Fatalf("expected unobserved service to be an idle candidate: %+v", got)
	}
	if got.RecommendedAction != "observe_before_idle" || got.Confidence != "low" {
		t.Fatalf("unexpected recommendation: %+v", got)
	}
}

func TestSummarizeAccessPatternsKeepsRecentlyAccessedServiceWarm(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-2 * time.Hour)
	summaries := summarizeAccessPatterns(testAccessServices(), []store.AccessPatternRow{
		{
			App:       "active-app",
			Process:   "web",
			Endpoint:  "https://active.example.test",
			Source:    "gateway",
			Hour:      10,
			Weekday:   2,
			Requests:  12,
			Successes: 12,
			FirstSeen: lastSeen.Add(-time.Hour),
			LastSeen:  lastSeen,
		},
	}, 14*24*time.Hour, 7*24*time.Hour, now)

	got := findAccessSummary(t, summaries, "active-app", "web")
	if got.IdleCandidate {
		t.Fatalf("expected recently accessed service to stay warm: %+v", got)
	}
	if got.RecommendedAction != "keep_warm" || got.TotalRequests != 12 {
		t.Fatalf("unexpected active summary: %+v", got)
	}
	if got.PeakHourUTC == nil || *got.PeakHourUTC != 10 {
		t.Fatalf("peak hour = %+v, want 10", got.PeakHourUTC)
	}
	if got.QuietForHours == nil || *got.QuietForHours < 1.9 || *got.QuietForHours > 2.1 {
		t.Fatalf("quiet hours = %+v, want about 2", got.QuietForHours)
	}
}

func TestSummarizeAccessPatternsFlagsQuietPastThreshold(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-9 * 24 * time.Hour)
	summaries := summarizeAccessPatterns(testAccessServices(), []store.AccessPatternRow{
		{
			App:       "active-app",
			Process:   "web",
			Source:    "cloudflared",
			Hour:      8,
			Weekday:   1,
			Requests:  3,
			Successes: 3,
			FirstSeen: lastSeen.Add(-time.Hour),
			LastSeen:  lastSeen,
		},
	}, 14*24*time.Hour, 7*24*time.Hour, now)

	got := findAccessSummary(t, summaries, "active-app", "web")
	if !got.IdleCandidate {
		t.Fatalf("expected quiet service to be an idle candidate: %+v", got)
	}
	if got.RecommendedAction != "consider_idle" || got.Confidence != "medium" {
		t.Fatalf("unexpected quiet recommendation: %+v", got)
	}
}

func testAccessServices() []model.ServiceManifestEntry {
	return []model.ServiceManifestEntry{
		{
			App:     "active-app",
			Process: "web",
			Type:    "service",
			Status:  "passing",
		},
	}
}

func findAccessSummary(t *testing.T, summaries []accessPatternSummary, app, process string) accessPatternSummary {
	t.Helper()
	for _, summary := range summaries {
		if summary.App == app && summary.Process == process {
			return summary
		}
	}
	t.Fatalf("missing summary for %s/%s: %+v", app, process, summaries)
	return accessPatternSummary{}
}
