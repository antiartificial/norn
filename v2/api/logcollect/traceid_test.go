package logcollect

import "testing"

func TestExtractTraceID(t *testing.T) {
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	for _, tc := range []struct{ name, line, want string }{
		{"snake", "GET / trace_id=" + id + " 200", id},
		{"camel", "traceId=" + id, id},
		{"json snake", `{"msg":"x","trace_id":"` + id + `"}`, id},
		{"json camel spaced", `{"traceId" : "` + id + `"}`, id},
		{"traceparent", "traceparent: 00-" + id + "-00f067aa0ba902b7-01", id},
		{"all zeros", "trace_id=00000000000000000000000000000000", ""},
		{"zero then valid", "trace_id=00000000000000000000000000000000 traceId=" + id, id},
		{"uppercase", "trace_id=4BF92F3577B34DA6A3CE929D0E0E4736", ""},
		{"too short", "trace_id=4bf92f3577b34da6", ""},
		{"too long", "trace_id=" + id + "ab", ""},
		{"prefixed key", "my_trace_id=" + id, ""},
		{"bare hex", id, ""},
		{"bad traceparent", "traceparent: 00-" + id + "-short-01", ""},
		{"empty", "", ""},
	} {
		if got := ExtractTraceID(tc.line); got != tc.want {
			t.Errorf("%s: ExtractTraceID = %q, want %q", tc.name, got, tc.want)
		}
	}
}
