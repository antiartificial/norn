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
