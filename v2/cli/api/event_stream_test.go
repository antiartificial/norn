package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEventStreamDiscoveryAndReplayURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events/info" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"protocolVersion":2,"bounds":{"oldestCursor":0,"latestCursor":10,"prunedThroughCursor":10,"retainedEvents":0},"retention":{"mode":"bounded","automaticPruning":false,"replayPageSize":500},"gapDetection":true}`)
	}))
	defer server.Close()

	client := New(server.URL)
	info, err := client.EventStreamInfo()
	if err != nil || info.ProtocolVersion != 2 || info.Bounds.PrunedThroughCursor != 10 || info.Retention.Mode != "bounded" {
		t.Fatalf("info=%#v err=%v", info, err)
	}
	url, err := client.EventStreamURLAfter(10)
	if err != nil || url != "ws"+server.URL[len("http"):]+"/api/v1/events?after=10" {
		t.Fatalf("url=%q err=%v", url, err)
	}
	if _, err := client.EventStreamURLAfter(-1); err == nil {
		t.Fatal("negative cursor was accepted")
	}
}
