package cmd

import (
	"strings"
	"testing"
	"time"

	"norn/v2/cli/api"
)

func TestParseLogTime(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{"": "", "15m": "2026-10-21T11:45:00Z", "1h30m": "2026-10-21T10:30:00Z", "2026-10-21T09:00:00+02:00": "2026-10-21T07:00:00Z", "2026-10-21T09:00:00.123456789Z": "2026-10-21T09:00:00.123456789Z"} {
		if got, err := parseLogTime("since", in, now); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"-5m", "yesterday", "2026-10-21", "15"} {
		if _, err := parseLogTime("since", bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBuildLogQuery(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	q, err := buildLogQuery("1h", "5m", "a1", "web", "web", "stderr", "boom", 10, true, now)
	want := api.LogQuery{Since: "2026-10-21T11:00:00Z", Until: "2026-10-21T11:55:00Z", Alloc: "a1", TaskGroup: "web", Task: "web", Stream: "stderr", Contains: "boom", Limit: 10, Direction: "forward"}
	if err != nil || q != want {
		t.Errorf("query = %+v, %v", q, err)
	}
	if _, err := buildLogQuery("x", "", "", "", "", "", "", 0, false, now); err == nil {
		t.Error("bad since accepted")
	}
}

func TestFormatLogEntry(t *testing.T) {
	at := time.Date(2026, 10, 21, 10, 0, 0, 500_000_000, time.UTC)
	line := formatLogEntry(api.LogEntry{Time: at, AllocID: "abcdef1234567890", Task: "web", Stream: "stdout", Line: "hello"})
	if line != "2026-10-21T10:00:00.500Z abcdef12 web stdout hello" {
		t.Errorf("line = %q", line)
	}
	gap := formatLogEntry(api.LogEntry{Time: at, AllocID: "a1", Task: "web", Stream: "stdout", Gap: "lost"})
	if !strings.Contains(gap, "\033[1;33m[gap] lost\033[0m") || !strings.HasPrefix(gap, "2026-10-21T10:00:00.500Z a1 web stdout ") {
		t.Errorf("gap = %q", gap)
	}
}
