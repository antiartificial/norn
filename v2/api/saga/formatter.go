package saga

import (
	"fmt"
	"strings"
)

// Formatter renders saga events as human-readable text.
type Formatter interface {
	Format(events []Event) string
}

// PlainFormatter renders events in a clean, readable format.
type PlainFormatter struct{}

func (f *PlainFormatter) Format(events []Event) string {
	var b strings.Builder
	for _, evt := range events {
		ts := evt.Timestamp.Format("15:04:05")
		icon := actionIcon(evt.Action)
		fmt.Fprintf(&b, "%s %s %s\n", ts, icon, evt.Message)
	}
	return b.String()
}

func actionIcon(action string) string {
	switch action {
	case "step.start":
		return "▶"
	case "step.complete":
		return "✓"
	case "step.failed":
		return "✗"
	default:
		return "·"
	}
}

// GrimdarkFormatter renders events in the Norse-themed grimdark style.
// Phase 5 — stub for now.
type GrimdarkFormatter struct{}

func (f *GrimdarkFormatter) Format(events []Event) string {
	return (&PlainFormatter{}).Format(events)
}
