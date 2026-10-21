package cmd

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"norn/v2/cli/api"
)

var mcpAllowWrites bool

func init() {
	mcpCmd.Flags().BoolVar(&mcpAllowWrites, "allow-writes", false, "Expose mutating tools (deploy, preflight, rollback, restart, scale, cron trigger, event ack)")
	rootCmd.AddCommand(mcpCmd)
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the Norn API as a Model Context Protocol server over stdio",
	Long: `Serve the Norn API to MCP clients (Claude Code, Claude Desktop, ...) over stdio.

Tools are read-only unless --allow-writes is set. Teardown, secret writes,
exec, snapshot restore, and token minting are never exposed.

Example (Claude Code):
  claude mcp add norn -e NORN_URL=https://norn.example -e NORN_TOKEN=... -- norn mcp`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		err := newMCPServer(client, mcpAllowWrites).Run(cmd.Context(), &mcp.StdioTransport{})
		if errors.Is(err, io.EOF) {
			// The client closed stdin: a normal shutdown.
			return nil
		}
		return err
	},
}

const mcpInstructions = `Norn is a control plane for self-hosted apps on Nomad/Consul.
Start with norn_health or norn_operator_inbox for an overview, norn_list_apps for
inventory, and norn_list_events / norn_active_incidents when diagnosing problems.
Deploys, rollbacks, and restarts are asynchronous: they return a saga/operation id;
follow up with norn_get_operation or norn_saga_events. Reuse the returned
idempotency_key when retrying a write whose outcome is unknown.`

// mcpIdentifier guards ids that are spliced into API paths.
var mcpIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)

func mcpID(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	if !mcpIdentifier.MatchString(value) || strings.Contains(value, "..") {
		return "", fmt.Errorf("invalid %s %q", field, value)
	}
	return value, nil
}

func mcpIdempotencyKey(supplied, prefix string) (string, error) {
	key := strings.TrimSpace(supplied)
	if key == "" {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("generate idempotency key: %w", err)
		}
		key = prefix + "-" + hex.EncodeToString(random)
	}
	if len(key) > 200 {
		return "", fmt.Errorf("idempotency_key must not exceed 200 characters")
	}
	return key, nil
}

func mcpLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	return min(limit, max)
}

// Tool inputs.

type mcpNoInput struct{}

type mcpAppInput struct {
	App string `json:"app" jsonschema:"app name"`
}

type mcpIDInput struct {
	ID string `json:"id" jsonschema:"record id"`
}

type mcpListEventsInput struct {
	App      string `json:"app,omitempty" jsonschema:"filter by app name"`
	Type     string `json:"type,omitempty" jsonschema:"filter by event type"`
	Severity string `json:"severity,omitempty" jsonschema:"filter by severity"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max events (default 50, max 500)"`
}

type mcpLimitInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"max results (default 50, max 500)"`
}

type mcpListOperationsInput struct {
	Active bool `json:"active,omitempty" jsonschema:"only queued or running operations"`
	Limit  int  `json:"limit,omitempty" jsonschema:"max results (default 50, max 500)"`
}

type mcpListDeploymentsInput struct {
	App string `json:"app,omitempty" jsonschema:"filter by app name"`
}

type mcpDeploymentStepsInput struct {
	DeploymentID string `json:"deployment_id" jsonschema:"deployment id from norn_list_deployments"`
}

type mcpSagaInput struct {
	SagaID string `json:"saga_id,omitempty" jsonschema:"return every event for this saga; otherwise recent events are listed"`
	App    string `json:"app,omitempty" jsonschema:"filter recent events by app (ignored with saga_id)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max recent events (default 50, max 500)"`
}

type mcpValidateInput struct {
	App           string `json:"app" jsonschema:"app name"`
	StrictSecrets bool   `json:"strict_secrets,omitempty" jsonschema:"treat missing secrets as errors"`
}

type mcpLogsInput struct {
	App         string `json:"app" jsonschema:"app name"`
	MaxLines    int    `json:"max_lines,omitempty" jsonschema:"stop after this many lines (default 200, max 2000)"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"stop after this many seconds (default 5, max 25)"`
}

type mcpQueryLogsInput struct {
	App       string `json:"app" jsonschema:"app name"`
	Since     string `json:"since,omitempty" jsonschema:"start: a duration like 15m (ago) or an RFC 3339 time (default 1h ago)"`
	Until     string `json:"until,omitempty" jsonschema:"end: a duration like 5m (ago) or an RFC 3339 time (default now; window at most 7 days)"`
	Alloc     string `json:"alloc,omitempty" jsonschema:"only this allocation id"`
	TaskGroup string `json:"task_group,omitempty" jsonschema:"only this task group"`
	Task      string `json:"task,omitempty" jsonschema:"only this task"`
	Stream    string `json:"stream,omitempty" jsonschema:"stdout or stderr"`
	Contains  string `json:"contains,omitempty" jsonschema:"only lines containing this text (case-sensitive, max 256 bytes)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max lines (default 200, max 2000)"`
	Direction string `json:"direction,omitempty" jsonschema:"backward (newest first, default) or forward (oldest first)"`
}

type mcpSearchTracesInput struct {
	App           string `json:"app" jsonschema:"app name"`
	Since         string `json:"since,omitempty" jsonschema:"look back this far, a Go duration such as 30m or 2h (default 1h)"`
	Limit         int    `json:"limit,omitempty" jsonschema:"max traces (default 20, max 100)"`
	MinDurationMs int    `json:"min_duration_ms,omitempty" jsonschema:"only traces slower than this many milliseconds"`
}

type mcpGetTraceInput struct {
	App     string `json:"app" jsonschema:"app name"`
	TraceID string `json:"trace_id" jsonschema:"32 lowercase hex character trace id from norn_search_traces or a log line"`
}

type mcpRefInput struct {
	App            string `json:"app" jsonschema:"app name"`
	Ref            string `json:"ref,omitempty" jsonschema:"git ref to use (default HEAD)"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"reuse when retrying an ambiguous failure; generated when omitted"`
}

type mcpAppKeyInput struct {
	App            string `json:"app" jsonschema:"app name"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"reuse when retrying an ambiguous failure; generated when omitted"`
}

type mcpScaleInput struct {
	App   string `json:"app" jsonschema:"app name"`
	Group string `json:"group" jsonschema:"process group to scale"`
	Count int    `json:"count" jsonschema:"desired instance count (0-100)"`
}

type mcpCronTriggerInput struct {
	App     string `json:"app" jsonschema:"app name"`
	Process string `json:"process" jsonschema:"cron process name"`
}

type mcpAckInput struct {
	ID   string `json:"id" jsonschema:"event id"`
	Note string `json:"note,omitempty" jsonschema:"acknowledgement note"`
}

func newMCPServer(c *api.Client, allowWrites bool) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "norn", Version: Version}, &mcp.ServerOptions{Instructions: mcpInstructions})

	readTool := func(name, desc string) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	}

	mcp.AddTool(s, readTool("norn_health", "Control plane health: API, Nomad, Consul and dependency status."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
			out, err := c.Health()
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_operator_inbox", "Operator inbox: what currently needs human attention."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
			out, err := c.OperatorInbox()
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_list_apps", "List apps with spec, Nomad status, health and allocations."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
			out, err := c.ListApps()
			return nil, map[string]any{"apps": out}, err
		})
	mcp.AddTool(s, readTool("norn_get_app", "Get one app's spec, Nomad status, health and allocations."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAppInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.GetApp(app)
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_validate_app", "Validate an app's infraspec, secrets and dependencies."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpValidateInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.ValidateApp(app, in.StrictSecrets)
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_service_manifest", "Service manifest: every service, its endpoints, instances and reachability."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
			out, err := c.ServiceManifest()
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_list_deployments", "List deployments, optionally for one app."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListDeploymentsInput) (*mcp.CallToolResult, any, error) {
			app := ""
			if in.App != "" {
				var err error
				if app, err = mcpID("app", in.App); err != nil {
					return nil, nil, err
				}
			}
			out, err := c.ListDeployments(app)
			return nil, map[string]any{"deployments": out}, err
		})
	mcp.AddTool(s, readTool("norn_deployment_steps", "Recorded stage checkpoints for a deploy or rollback."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpDeploymentStepsInput) (*mcp.CallToolResult, any, error) {
			id, err := mcpID("deployment_id", in.DeploymentID)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.DeploymentSteps(id)
			return nil, map[string]any{"steps": out}, err
		})
	mcp.AddTool(s, readTool("norn_list_operations", "List control plane operations (deploys, restarts, maintenance)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListOperationsInput) (*mcp.CallToolResult, any, error) {
			out, err := c.ListOperations(in.Active, mcpLimit(in.Limit, 50, 500))
			return nil, map[string]any{"operations": out}, err
		})
	mcp.AddTool(s, readTool("norn_get_operation", "Get one operation's status and result."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpIDInput) (*mcp.CallToolResult, any, error) {
			id, err := mcpID("id", in.ID)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.GetOperation(id)
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_saga_events", "Saga event log: all events for one saga, or recent events (optionally per app)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSagaInput) (*mcp.CallToolResult, any, error) {
			if in.SagaID != "" {
				id, err := mcpID("saga_id", in.SagaID)
				if err != nil {
					return nil, nil, err
				}
				out, err := c.GetSagaEvents(id)
				return nil, map[string]any{"events": out}, err
			}
			app := ""
			if in.App != "" {
				var err error
				if app, err = mcpID("app", in.App); err != nil {
					return nil, nil, err
				}
			}
			out, err := c.ListRecentSaga(app, mcpLimit(in.Limit, 50, 500))
			return nil, map[string]any{"events": out}, err
		})
	mcp.AddTool(s, readTool("norn_list_events", "List beacon events (alerts, failures, notices) with optional filters."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListEventsInput) (*mcp.CallToolResult, any, error) {
			out, total, err := c.ListEvents(in.App, in.Type, in.Severity, mcpLimit(in.Limit, 50, 500))
			return nil, map[string]any{"events": out, "total": total}, err
		})
	mcp.AddTool(s, readTool("norn_get_event", "Get one beacon event."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpIDInput) (*mcp.CallToolResult, any, error) {
			id, err := mcpID("id", in.ID)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.GetEvent(id)
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_active_incidents", "Active incidents grouped from open events."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpLimitInput) (*mcp.CallToolResult, any, error) {
			out, err := c.ListActiveIncidents(mcpLimit(in.Limit, 50, 500))
			return nil, map[string]any{"incidents": out}, err
		})
	mcp.AddTool(s, readTool("norn_logs", "Tail recent log output from a running app (bounded by line count and time)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpLogsInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			lines, err := mcpTailLogs(ctx, c, app, mcpLimit(in.MaxLines, 200, 2000), time.Duration(mcpLimit(in.WaitSeconds, 5, 25))*time.Second)
			return nil, map[string]any{"lines": lines}, err
		})
	mcp.AddTool(s, readTool("norn_query_logs", "Query an app's historical log lines by time window, allocation, task group, task, stream and substring. Each entry carries its allocation and a traceId when the line has one; gap entries mark lost output."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpQueryLogsInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			for field, value := range map[string]string{"alloc": in.Alloc, "task_group": in.TaskGroup, "task": in.Task} {
				if value != "" {
					if _, err := mcpID(field, value); err != nil {
						return nil, nil, err
					}
				}
			}
			if in.Direction != "" && in.Direction != "backward" && in.Direction != "forward" {
				return nil, nil, fmt.Errorf("direction must be backward or forward")
			}
			q, err := buildLogQuery(in.Since, in.Until, in.Alloc, in.TaskGroup, in.Task, in.Stream, in.Contains, mcpLimit(in.Limit, 200, 2000), in.Direction == "forward", time.Now())
			if err != nil {
				return nil, nil, err
			}
			out, err := c.QueryLogs(app, q)
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_search_traces", "Search recent distributed traces of an app (requires tracing to be configured on the server)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSearchTracesInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			since := time.Hour
			if in.Since != "" {
				if since, err = time.ParseDuration(in.Since); err != nil || since <= 0 {
					return nil, nil, fmt.Errorf("invalid since %q", in.Since)
				}
			}
			out, err := c.SearchTraces(app, time.Now().Add(-since), mcpLimit(in.Limit, 20, 100), max(in.MinDurationMs, 0))
			return nil, map[string]any{"traces": out}, err
		})
	mcp.AddTool(s, readTool("norn_get_trace", "Get every span of one trace of an app, with timings, status and attributes."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetTraceInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			if !traceIDPattern.MatchString(in.TraceID) {
				return nil, nil, fmt.Errorf("invalid trace_id %q: must be 32 lowercase hex characters", in.TraceID)
			}
			out, err := c.GetTrace(app, in.TraceID)
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_cron_history", "Cron process state and recent runs for an app."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAppInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.CronHistory(app)
			return nil, map[string]any{"crons": out}, err
		})
	mcp.AddTool(s, readTool("norn_list_snapshots", "Database snapshots available for an app."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAppInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.ListSnapshots(app)
			return nil, map[string]any{"snapshots": out}, err
		})
	mcp.AddTool(s, readTool("norn_list_secret_keys", "Secret key names configured for an app (values are never returned)."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAppInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.ListSecrets(app)
			return nil, map[string]any{"keys": out}, err
		})
	mcp.AddTool(s, readTool("norn_fleet_inventory", "Fleet inventory: pools, hosts and capacity."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
			out, err := c.FleetInventory()
			return nil, out, err
		})
	mcp.AddTool(s, readTool("norn_tuning_recommendations", "CPU/memory/scale tuning recommendations from observed usage."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
			out, err := c.TuningRecommendations()
			return nil, map[string]any{"recommendations": out}, err
		})

	if allowWrites {
		addMCPWriteTools(s, c)
	}
	return s
}

func addMCPWriteTools(s *mcp.Server, c *api.Client) {
	destructive, safe := true, false
	writeTool := func(name, desc string, isDestructive *bool, idempotent bool) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: isDestructive, IdempotentHint: idempotent}}
	}

	mcp.AddTool(s, writeTool("norn_preflight", "Queue a preflight check of an app at a git ref without deploying.", &safe, true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpRefInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			key, err := mcpIdempotencyKey(in.IdempotencyKey, "norn-mcp-preflight")
			if err != nil {
				return nil, nil, err
			}
			out, err := c.Preflight(app, mcpRef(in.Ref), key)
			return nil, map[string]any{"accepted": out, "idempotency_key": key}, mcpKeyErr(err, key)
		})
	mcp.AddTool(s, writeTool("norn_deploy", "Deploy an app at a git ref. Asynchronous: follow the returned operation.", &destructive, true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpRefInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			key, err := mcpIdempotencyKey(in.IdempotencyKey, "norn-mcp-deploy")
			if err != nil {
				return nil, nil, err
			}
			out, err := c.Deploy(app, mcpRef(in.Ref), key)
			return nil, map[string]any{"accepted": out, "idempotency_key": key}, mcpKeyErr(err, key)
		})
	mcp.AddTool(s, writeTool("norn_rollback", "Roll an app back to its previous release. Asynchronous.", &destructive, true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAppKeyInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			key, err := mcpIdempotencyKey(in.IdempotencyKey, "norn-mcp-rollback")
			if err != nil {
				return nil, nil, err
			}
			out, err := c.Rollback(app, key)
			return nil, map[string]any{"accepted": out, "idempotency_key": key}, mcpKeyErr(err, key)
		})
	mcp.AddTool(s, writeTool("norn_restart", "Restart an app's allocations.", &destructive, true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAppKeyInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			key, err := mcpIdempotencyKey(in.IdempotencyKey, "norn-mcp-restart")
			if err != nil {
				return nil, nil, err
			}
			out, err := c.Restart(app, key)
			return nil, map[string]any{"operation": out, "idempotency_key": key}, mcpKeyErr(err, key)
		})
	mcp.AddTool(s, writeTool("norn_scale", "Set the instance count of an app's process group.", &destructive, true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpScaleInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			if in.Group == "" || in.Count < 0 || in.Count > 100 {
				return nil, nil, fmt.Errorf("group is required and count must be between 0 and 100")
			}
			if err := c.Scale(app, in.Group, in.Count); err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"app": app, "group": in.Group, "count": in.Count}, nil
		})
	mcp.AddTool(s, writeTool("norn_cron_trigger", "Run an app's cron process once now.", &safe, false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpCronTriggerInput) (*mcp.CallToolResult, any, error) {
			app, err := mcpID("app", in.App)
			if err != nil {
				return nil, nil, err
			}
			if err := c.CronTrigger(app, in.Process); err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"app": app, "process": in.Process, "triggered": true}, nil
		})
	mcp.AddTool(s, writeTool("norn_acknowledge_event", "Acknowledge a beacon event.", &safe, true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAckInput) (*mcp.CallToolResult, any, error) {
			id, err := mcpID("id", in.ID)
			if err != nil {
				return nil, nil, err
			}
			out, err := c.AcknowledgeEvent(id, "norn-mcp", in.Note)
			return nil, out, err
		})
}

func mcpRef(ref string) string {
	if ref = strings.TrimSpace(ref); ref == "" {
		return "HEAD"
	}
	return ref
}

// mcpKeyErr keeps the idempotency key visible on failure so an ambiguous
// write can be retried safely.
func mcpKeyErr(err error, key string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w (idempotency_key: %s; reuse it to retry)", err, key)
}

func mcpTailLogs(ctx context.Context, c *api.Client, app string, maxLines int, wait time.Duration) ([]string, error) {
	reader, err := c.StreamLogs(app)
	if err != nil {
		return nil, fmt.Errorf("stream logs: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	go func() {
		<-ctx.Done()
		reader.Close()
	}()

	lines := []string{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for len(lines) < maxLines && scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	// Closing the stream on timeout surfaces as a read error; that is the
	// normal end of a bounded tail, not a failure.
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return lines, err
	}
	return lines, nil
}
