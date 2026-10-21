package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"norn/v2/cli/api"
)

type mcpFakeNorn struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]string
	auth     []string
}

func (f *mcpFakeNorn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.bodies[r.Method+" "+r.URL.Path] = string(body)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()

	switch {
	case r.URL.Path == "/api/apps/web":
		fmt.Fprint(w, `{"spec":{"name":"web"},"nomadStatus":"running","healthy":true}`)
	case r.URL.Path == "/api/apps/web/deploy":
		fmt.Fprint(w, `{"sagaId":"saga-1","operationId":"op-1","status":"queued"}`)
	case r.URL.Path == "/api/v1/apps/web/logs/query":
		fmt.Fprint(w, `{"app":"web","source":"loki","truncated":false,"entries":[{"time":"2026-10-21T10:00:00.5Z","allocId":"alloc-12345678","task":"web","stream":"stdout","line":"hello","traceId":"4bf92f3577b34da6a3ce929d0e0e4736"}]}`)
	case r.URL.Path == "/api/apps/web/logs":
		w.(http.Flusher).Flush()
		for i := range 5 {
			fmt.Fprintf(w, "line %d\n", i)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	default:
		http.NotFound(w, r)
	}
}

func connectMCPTest(t *testing.T, allowWrites bool) (*mcp.ClientSession, *mcpFakeNorn) {
	t.Helper()
	fake := &mcpFakeNorn{bodies: map[string]string{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := api.New(srv.URL)
	c.Token = "test-token"
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := newMCPServer(c, allowWrites).Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, fake
}

func mcpToolNames(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func callMCPTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if !res.IsError {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &out)
	}
	return res, out
}

func mcpResultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func TestMCPReadOnlyByDefault(t *testing.T) {
	session, _ := connectMCPTest(t, false)
	tools := mcpToolNames(t, session)
	if _, ok := tools["norn_get_app"]; !ok {
		t.Fatal("norn_get_app missing")
	}
	for name, tool := range tools {
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s exposed without --allow-writes", name)
		}
	}
	for _, name := range []string{"norn_deploy", "norn_rollback", "norn_restart", "norn_scale"} {
		if _, ok := tools[name]; ok {
			t.Errorf("%s exposed without --allow-writes", name)
		}
	}
}

func TestMCPAllowWritesAddsMutatingTools(t *testing.T) {
	session, _ := connectMCPTest(t, true)
	tools := mcpToolNames(t, session)
	deploy, ok := tools["norn_deploy"]
	if !ok {
		t.Fatal("norn_deploy missing with --allow-writes")
	}
	if deploy.Annotations.ReadOnlyHint || deploy.Annotations.DestructiveHint == nil || !*deploy.Annotations.DestructiveHint {
		t.Errorf("norn_deploy annotations = %+v, want destructive write", deploy.Annotations)
	}
	for _, name := range []string{"norn_teardown", "norn_exec", "norn_update_secrets"} {
		if _, ok := tools[name]; ok {
			t.Errorf("%s must never be exposed", name)
		}
	}
}

func TestMCPGetAppUsesTokenAndReturnsStructuredContent(t *testing.T) {
	session, fake := connectMCPTest(t, false)
	res, out := callMCPTool(t, session, "norn_get_app", map[string]any{"app": "web"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", mcpResultText(res))
	}
	if out["nomadStatus"] != "running" {
		t.Errorf("structured content = %v", out)
	}
	if got := fake.auth[0]; got != "Bearer test-token" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestMCPRejectsPathTraversalIDs(t *testing.T) {
	session, fake := connectMCPTest(t, false)
	for _, app := range []string{"../admin", "web/../../x", "a b", ""} {
		res, _ := callMCPTool(t, session, "norn_get_app", map[string]any{"app": app})
		if !res.IsError {
			t.Errorf("app %q was accepted", app)
		}
	}
	if len(fake.requests) != 0 {
		t.Errorf("invalid ids reached the API: %v", fake.requests)
	}
}

func TestMCPDeployReturnsIdempotencyKey(t *testing.T) {
	session, fake := connectMCPTest(t, true)
	res, out := callMCPTool(t, session, "norn_deploy", map[string]any{"app": "web"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", mcpResultText(res))
	}
	key, _ := out["idempotency_key"].(string)
	if !strings.HasPrefix(key, "norn-mcp-deploy-") {
		t.Errorf("idempotency_key = %q", key)
	}
	if body := fake.bodies["POST /api/apps/web/deploy"]; !strings.Contains(body, `"HEAD"`) {
		t.Errorf("deploy body = %s, want default ref HEAD", body)
	}

	res, out = callMCPTool(t, session, "norn_deploy", map[string]any{"app": "web", "ref": "v1.2.3", "idempotency_key": "retry-me"})
	if res.IsError || out["idempotency_key"] != "retry-me" {
		t.Errorf("supplied key not reused: %v %s", out, mcpResultText(res))
	}
}

func TestMCPLogsStopAtMaxLines(t *testing.T) {
	session, _ := connectMCPTest(t, false)
	res, out := callMCPTool(t, session, "norn_logs", map[string]any{"app": "web", "max_lines": 3, "wait_seconds": 2})
	if res.IsError {
		t.Fatalf("unexpected error: %s", mcpResultText(res))
	}
	lines, _ := out["lines"].([]any)
	if len(lines) != 3 || lines[0] != "line 0" {
		t.Errorf("lines = %v", lines)
	}
}

func TestMCPLogsStopAtDeadline(t *testing.T) {
	session, _ := connectMCPTest(t, false)
	res, out := callMCPTool(t, session, "norn_logs", map[string]any{"app": "web", "max_lines": 100, "wait_seconds": 1})
	if res.IsError {
		t.Fatalf("unexpected error: %s", mcpResultText(res))
	}
	if lines, _ := out["lines"].([]any); len(lines) != 5 {
		t.Errorf("lines = %v, want the 5 emitted before the deadline", lines)
	}
}

func TestMCPQueryLogsMapsParamsAndIsReadOnly(t *testing.T) {
	session, fake := connectMCPTest(t, false)
	if tool := mcpToolNames(t, session)["norn_query_logs"]; tool == nil || !tool.Annotations.ReadOnlyHint {
		t.Fatalf("norn_query_logs missing or not read-only: %+v", tool)
	}
	res, out := callMCPTool(t, session, "norn_query_logs", map[string]any{"app": "web", "since": "15m", "alloc": "alloc-1", "task_group": "web", "task": "web",
		"stream": "stderr", "contains": "a&b=c", "limit": 99999, "direction": "forward"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", mcpResultText(res))
	}
	entries, _ := out["entries"].([]any)
	if out["source"] != "loki" || len(entries) != 1 || entries[0].(map[string]any)["traceId"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("out = %v", out)
	}
	request := fake.requests[len(fake.requests)-1]
	path, rawQuery, _ := strings.Cut(request, "?")
	values, _ := url.ParseQuery(rawQuery)
	if path != "GET /api/v1/apps/web/logs/query" || values.Get("alloc") != "alloc-1" || values.Get("taskGroup") != "web" || values.Get("task") != "web" ||
		values.Get("stream") != "stderr" || values.Get("contains") != "a&b=c" || values.Get("limit") != "2000" || values.Get("direction") != "forward" {
		t.Errorf("request = %s", request)
	}
	since, err := time.Parse(time.RFC3339Nano, values.Get("since"))
	if err != nil || time.Since(since) < 14*time.Minute || time.Since(since) > 16*time.Minute {
		t.Errorf("since = %q", values.Get("since"))
	}
	for _, bad := range []map[string]any{
		{"app": "../x"}, {"app": "web", "alloc": "a/b"}, {"app": "web", "task": "x y"}, {"app": "web", "since": "soon"}, {"app": "web", "direction": "up"},
	} {
		if res, _ := callMCPTool(t, session, "norn_query_logs", bad); !res.IsError {
			t.Errorf("%v accepted", bad)
		}
	}
}
