package cmd

import (
	"strings"
	"testing"

	"norn/v2/cli/api"
)

func TestRenderTraceTreeNestsChildrenAndOrphans(t *testing.T) {
	out := renderTraceTree(&api.Trace{OmittedSpans: 2, Spans: []api.TraceSpan{
		{SpanID: "a", Name: "GET /", Service: "web", DurationMs: 12.5},
		{SpanID: "b", ParentSpanID: "a", Name: "query", Service: "web", DurationMs: 2, Status: "error", StatusMsg: "timeout"},
		{SpanID: "c", ParentSpanID: "gone", Name: "orphan", Service: "web", DurationMs: 0.5},
	}})
	for _, want := range []string{"GET /", "  query", "ERROR", "timeout", "12.5ms", "500us", "orphan", "2 span(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "GET /") > strings.Index(out, "query") {
		t.Errorf("child printed before parent:\n%s", out)
	}
}
