package handler

import (
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestAccessHostnameMapIncludesPublicServiceEndpoints(t *testing.T) {
	services := []model.ServiceManifestEntry{
		{
			App:     "field-harbor",
			Process: "web",
			Type:    "service",
			Endpoints: []model.Endpoint{
				{URL: "https://harbor.example.com"},
				{URL: "http://127.0.0.1:8080"},
				{URL: "https://gitea.internal"},
				{URL: "https://aarons-mac-mini.tail113139.ts.net"},
			},
		},
		{
			App:     "field-harbor",
			Process: "sync",
			Type:    "cron",
			Endpoints: []model.Endpoint{
				{URL: "https://cron.example.com"},
			},
		},
	}

	hostMap := accessHostnameMap(services)
	target, ok := hostMap["harbor.example.com"]
	if !ok {
		t.Fatalf("expected public hostname in map: %#v", hostMap)
	}
	if target.App != "field-harbor" || target.Process != "web" {
		t.Fatalf("target = %#v, want field-harbor/web", target)
	}
	if _, ok := hostMap["127.0.0.1"]; ok {
		t.Fatalf("loopback endpoint should not be mapped: %#v", hostMap)
	}
	if _, ok := hostMap["gitea.internal"]; ok {
		t.Fatalf("internal endpoint should not be mapped: %#v", hostMap)
	}
	if _, ok := hostMap["aarons-mac-mini.tail113139.ts.net"]; ok {
		t.Fatalf("tailnet endpoint should not be mapped: %#v", hostMap)
	}
	if _, ok := hostMap["cron.example.com"]; ok {
		t.Fatalf("cron endpoint should not be mapped: %#v", hostMap)
	}
}

func TestPublicEndpointHostnameFiltersTailnetAndPrivateEndpoints(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "https://harbor.example.com", want: "harbor.example.com"},
		{raw: "https://aarons-mac-mini.tail113139.ts.net", want: ""},
		{raw: "http://100.88.12.4:7070", want: ""},
		{raw: "http://127.0.0.1:8080", want: ""},
		{raw: "https://gitea.internal", want: ""},
	}
	for _, tt := range tests {
		if got := publicEndpointHostname(tt.raw); got != tt.want {
			t.Fatalf("publicEndpointHostname(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestCloudflareSyncChunksSplitsLongWindows(t *testing.T) {
	since := time.Date(2026, 6, 1, 6, 0, 0, 0, time.UTC)
	until := since.Add(49 * time.Hour)
	chunks := cloudflareSyncChunks(since, until, 24*time.Hour)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	if chunks[0].Since != since || chunks[0].Until != since.Add(24*time.Hour) {
		t.Fatalf("first chunk = %#v", chunks[0])
	}
	if chunks[1].Since != since.Add(24*time.Hour) || chunks[1].Until != since.Add(48*time.Hour) {
		t.Fatalf("second chunk = %#v", chunks[1])
	}
	if chunks[2].Since != since.Add(48*time.Hour) || chunks[2].Until != until {
		t.Fatalf("third chunk = %#v", chunks[2])
	}
}

func TestCloudflareEffectiveSinceClampsToLookback(t *testing.T) {
	until := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	got := cloudflareEffectiveSince(until.Add(-14*24*time.Hour), until, 7*24*time.Hour)
	want := until.Add(-7 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("effective since = %s, want %s", got, want)
	}

	inside := until.Add(-2 * 24 * time.Hour)
	got = cloudflareEffectiveSince(inside, until, 7*24*time.Hour)
	if !got.Equal(inside) {
		t.Fatalf("effective since inside lookback = %s, want %s", got, inside)
	}
}

func TestParseCloudflareLogpushEventsSupportsArrayAndNDJSON(t *testing.T) {
	arrayPayload := []byte(`[
		{"ClientRequestHost":"harbor.example.com","EdgeStartTimestamp":"2026-06-17T10:15:00Z","EdgeResponseStatus":200},
		{"ClientRequestHost":"harbor.example.com","EdgeStartTimestamp":"2026-06-17T10:16:00Z","EdgeResponseStatus":503},
		{"EdgeResponseStatus":200}
	]`)
	events, invalid, err := parseCloudflareLogpushEvents(arrayPayload)
	if err != nil {
		t.Fatalf("parse array: %v", err)
	}
	if len(events) != 2 || invalid != 1 {
		t.Fatalf("array parse got events=%d invalid=%d", len(events), invalid)
	}
	if events[0].Host != "harbor.example.com" || events[1].Status != 503 {
		t.Fatalf("events = %#v", events)
	}
	wantTime := time.Date(2026, 6, 17, 10, 15, 0, 0, time.UTC)
	if !events[0].ObservedAt.Equal(wantTime) {
		t.Fatalf("observedAt = %s, want %s", events[0].ObservedAt, wantTime)
	}

	ndjsonPayload := []byte("{\"clientRequestHost\":\"trove.example.com\",\"edgeResponseStatus\":404}\n{\"host\":\"api.example.com\"}\n")
	events, invalid, err = parseCloudflareLogpushEvents(ndjsonPayload)
	if err != nil {
		t.Fatalf("parse ndjson: %v", err)
	}
	if len(events) != 2 || invalid != 0 {
		t.Fatalf("ndjson parse got events=%d invalid=%d", len(events), invalid)
	}
	if events[0].Host != "trove.example.com" || events[0].Status != 404 {
		t.Fatalf("ndjson event = %#v", events[0])
	}
	if events[1].Host != "api.example.com" || events[1].Status != 200 {
		t.Fatalf("default status event = %#v", events[1])
	}
}
