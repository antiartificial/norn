package engine

import (
	"testing"
	"time"
)

func TestParseMeminfo(t *testing.T) {
	content := `MemTotal:        2048000 kB
MemFree:          512000 kB
MemAvailable:    1024000 kB
Buffers:          128000 kB
Cached:           256000 kB
`
	usage, total := parseMeminfo(content)
	if total != 2048000*1024 {
		t.Errorf("total = %d, want %d", total, 2048000*1024)
	}
	expectedUsage := (2048000 - 1024000) * 1024
	if usage != uint64(expectedUsage) {
		t.Errorf("usage = %d, want %d", usage, expectedUsage)
	}
}

func TestParseMeminfo_Empty(t *testing.T) {
	usage, total := parseMeminfo("")
	if usage != 0 || total != 0 {
		t.Errorf("empty meminfo: usage=%d total=%d", usage, total)
	}
}

func TestParseProcStat(t *testing.T) {
	content := `cpu  100 20 30 800 50 0 0 0 0 0
cpu0 50 10 15 400 25 0 0 0 0 0
`
	pct := parseProcStat(content)
	// active = 100+20+30 = 150, total = 100+20+30+800+50 = 1000
	expected := 15.0
	if pct != expected {
		t.Errorf("cpu = %.1f%%, want %.1f%%", pct, expected)
	}
}

func TestParseProcStat_Empty(t *testing.T) {
	pct := parseProcStat("")
	if pct != 0 {
		t.Errorf("empty stat: %.1f%%", pct)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		hours int
		want  string
	}{
		{0, "0m"},
		{1, "1h0m"},
		{25, "1d1h"},
		{48, "2d0h"},
	}
	for _, tt := range tests {
		got := formatDuration(time.Duration(tt.hours) * time.Hour)
		if got != tt.want {
			t.Errorf("formatDuration(%dh) = %q, want %q", tt.hours, got, tt.want)
		}
	}
}
