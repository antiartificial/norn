package cmd

import "testing"

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
