package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"norn/v2/cli/api"
)

var logQueryFlags struct {
	since, until, alloc, taskGroup, task, stream, grep string
	limit                                              int
	forward                                            bool
}

func init() {
	rootCmd.AddCommand(logsCmd)
	flags := logsCmd.Flags()
	flags.StringVar(&logQueryFlags.since, "since", "", "query from this time: a duration like 15m or an RFC 3339 time")
	flags.StringVar(&logQueryFlags.until, "until", "", "query up to this time: a duration like 5m (ago) or an RFC 3339 time")
	flags.StringVar(&logQueryFlags.alloc, "alloc", "", "only this allocation id")
	flags.StringVar(&logQueryFlags.taskGroup, "task-group", "", "only this task group")
	flags.StringVar(&logQueryFlags.task, "task", "", "only this task")
	flags.StringVar(&logQueryFlags.stream, "stream", "", "only stdout or stderr")
	flags.StringVar(&logQueryFlags.grep, "grep", "", "only lines containing this text (case-sensitive)")
	flags.IntVar(&logQueryFlags.limit, "limit", 0, "maximum lines (default 500, max 5000)")
	flags.BoolVar(&logQueryFlags.forward, "forward", false, "oldest first (default newest first)")
}

var logsCmd = &cobra.Command{
	Use:   "logs <app>",
	Short: "Stream live logs, or query history with --since/--until/--alloc/--grep/...",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		// Any query flag switches from the live stream to the query endpoint.
		queried := false
		cmd.Flags().Visit(func(*pflag.Flag) { queried = true })
		if queried {
			return runLogQuery(appID)
		}

		reader, err := client.StreamLogs(appID)
		if err != nil {
			return fmt.Errorf("stream logs: %w", err)
		}
		defer reader.Close()

		_, err = io.Copy(os.Stdout, reader)
		return err
	},
}

// parseLogTime turns a duration ("15m", meaning that long ago) or an RFC 3339
// time into the RFC 3339 time the query endpoint takes.
func parseLogTime(field, value string, now time.Time) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		if d < 0 {
			return "", fmt.Errorf("%s must not be negative", field)
		}
		return now.Add(-d).UTC().Format(time.RFC3339Nano), nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("%s must be a duration like 15m or an RFC 3339 time", field)
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}

func buildLogQuery(since, until, alloc, taskGroup, task, stream, contains string, limit int, forward bool, now time.Time) (api.LogQuery, error) {
	q := api.LogQuery{Alloc: alloc, TaskGroup: taskGroup, Task: task, Stream: stream, Contains: contains, Limit: limit}
	var err error
	if q.Since, err = parseLogTime("since", since, now); err != nil {
		return q, err
	}
	if q.Until, err = parseLogTime("until", until, now); err != nil {
		return q, err
	}
	if forward {
		q.Direction = "forward"
	}
	return q, nil
}

func runLogQuery(appID string) error {
	f := logQueryFlags
	q, err := buildLogQuery(f.since, f.until, f.alloc, f.taskGroup, f.task, f.stream, f.grep, f.limit, f.forward, time.Now())
	if err != nil {
		return err
	}
	result, err := client.QueryLogs(appID, q)
	if err != nil {
		return fmt.Errorf("query logs: %w", err)
	}
	for _, entry := range result.Entries {
		fmt.Println(formatLogEntry(entry))
	}
	if result.Truncated {
		fmt.Fprintln(os.Stderr, "(more lines matched; raise --limit or narrow the window)")
	}
	return nil
}

// formatLogEntry prints `time alloc[:8] task stream line`; gaps are
// highlighted so lost output is never mistaken for quiet.
func formatLogEntry(e api.LogEntry) string {
	alloc := e.AllocID
	if len(alloc) > 8 {
		alloc = alloc[:8]
	}
	prefix := fmt.Sprintf("%s %s %s %s ", e.Time.UTC().Format("2006-01-02T15:04:05.000Z"), alloc, e.Task, e.Stream)
	if e.Gap != "" {
		return prefix + "\033[1;33m[gap] " + e.Gap + "\033[0m"
	}
	return prefix + e.Line
}
