package cmd

import (
	"testing"

	"norn/v2/cli/api"
)

func TestCountActiveOperationsExcludesMaintenanceReceipt(t *testing.T) {
	operations := []api.Operation{{ID: "self"}, {ID: "other"}}
	if got := countActiveOperations(operations, " self "); got != 1 {
		t.Fatalf("active operations = %d, want 1", got)
	}
	if got := countActiveOperations(operations, ""); got != 2 {
		t.Fatalf("active operations without exclusion = %d, want 2", got)
	}
}

func TestExtractJSONObjectSkipsLogPreamble(t *testing.T) {
	raw := `time=2026-06-05T04:02:19Z level=INFO msg="connected"
{
  "dry_run": true,
  "nested": {"value": "brace } in string"}
}
time=2026-06-05T04:02:20Z level=INFO msg="closed"`

	got, err := extractJSONObject(raw)
	if err != nil {
		t.Fatalf("extractJSONObject: %v", err)
	}
	want := "{\n  \"dry_run\": true,\n  \"nested\": {\"value\": \"brace } in string\"}\n}"
	if got != want {
		t.Fatalf("json = %q, want %q", got, want)
	}
}

func TestExtractJSONObjectReportsMissingObject(t *testing.T) {
	_, err := extractJSONObject("time=2026-06-05T04:02:19Z level=INFO")
	if err == nil {
		t.Fatal("expected error")
	}
}
