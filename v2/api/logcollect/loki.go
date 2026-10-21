package logcollect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ParseLokiOrigin validates a Loki origin (scheme and host only), like the
// other trusted monitoring origins.
func ParseLokiOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("invalid Loki origin %q: want http(s)://host[:port] without credentials, path or query", raw)
	}
	return u, nil
}

// LokiClient talks to one Loki (>= 3.0, for structured metadata).
type LokiClient struct {
	Origin *url.URL
	Tenant string // X-Scope-OrgID
	Token  string // bearer
	HTTP   *http.Client
}

// NewLokiClient validates the origin and returns a client.
func NewLokiClient(origin, tenant, token string) (*LokiClient, error) {
	u, err := ParseLokiOrigin(origin)
	if err != nil {
		return nil, err
	}
	return &LokiClient{Origin: u, Tenant: tenant, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}, nil
}

// LokiStatusError is a non-2xx answer from Loki.
type LokiStatusError struct {
	Code int
	Body string
}

func (e *LokiStatusError) Error() string {
	return fmt.Sprintf("loki returned HTTP %d: %s", e.Code, e.Body)
}

// Retryable reports whether the same request may later succeed.
func (e *LokiStatusError) Retryable() bool {
	return e.Code >= 500 || e.Code == http.StatusTooManyRequests || e.Code == http.StatusRequestTimeout
}

func (c *LokiClient) do(req *http.Request) ([]byte, error) {
	if c.Tenant != "" {
		req.Header.Set("X-Scope-OrgID", c.Tenant)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		message := strings.TrimSpace(string(body))
		if len(message) > 300 {
			message = message[:300]
		}
		return nil, &LokiStatusError{Code: resp.StatusCode, Body: message}
	}
	return body, nil
}

// LokiLine is one pushed line with its structured metadata.
type LokiLine struct {
	Time     time.Time
	Line     string
	Metadata map[string]string
}

// Push sends lines of one label set. A nil error means Loki accepted them.
func (c *LokiClient) Push(ctx context.Context, labels map[string]string, lines []LokiLine) error {
	values := make([][]any, 0, len(lines))
	for _, line := range lines {
		values = append(values, []any{strconv.FormatInt(line.Time.UnixNano(), 10), line.Line, line.Metadata})
	}
	body, err := json.Marshal(map[string]any{"streams": []any{map[string]any{"stream": labels, "values": values}}})
	if err != nil {
		return err
	}
	u := *c.Origin
	u.Path = "/loki/api/v1/push"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	_, err = c.do(req)
	return err
}

// LokiEntry is one queried line. Labels are the stream labels; Metadata is
// the structured metadata, from the categorized response when Loki sends it
// and otherwise recovered from the stream map by the caller's field lookup.
type LokiEntry struct {
	Time     time.Time
	Line     string
	Labels   map[string]string
	Metadata map[string]string
}

// Field returns a structured metadata value, falling back to the stream
// labels (older response encodings merge metadata into the stream map).
func (e LokiEntry) Field(name string) string {
	if value, ok := e.Metadata[name]; ok {
		return value
	}
	return e.Labels[name]
}

// QueryRange runs a LogQL range query and returns the entries in Loki's
// order for direction (merged across the returned streams).
func (c *LokiClient) QueryRange(ctx context.Context, logql string, start, end time.Time, limit int, backward bool) ([]LokiEntry, error) {
	direction := "forward"
	if backward {
		direction = "backward"
	}
	u := *c.Origin
	u.Path = "/loki/api/v1/query_range"
	u.RawQuery = url.Values{"query": {logql}, "start": {strconv.FormatInt(start.UnixNano(), 10)}, "end": {strconv.FormatInt(end.UnixNano(), 10)},
		"limit": {strconv.Itoa(limit)}, "direction": {direction}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Loki-Response-Encoding-Flags", "categorize-labels")
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string   `json:"stream"`
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode loki response: %w", err)
	}
	if parsed.Status != "success" || parsed.Data.ResultType != "streams" {
		return nil, errors.New("unexpected loki response")
	}
	var entries []LokiEntry
	for _, stream := range parsed.Data.Result {
		for _, value := range stream.Values {
			if len(value) < 2 {
				continue
			}
			var stamp, line string
			if json.Unmarshal(value[0], &stamp) != nil || json.Unmarshal(value[1], &line) != nil {
				continue
			}
			nanos, err := strconv.ParseInt(stamp, 10, 64)
			if err != nil {
				continue
			}
			entry := LokiEntry{Time: time.Unix(0, nanos).UTC(), Line: line, Labels: stream.Stream}
			if len(value) > 2 {
				var extra struct {
					StructuredMetadata map[string]string `json:"structuredMetadata"`
				}
				if json.Unmarshal(value[2], &extra) == nil {
					entry.Metadata = extra.StructuredMetadata
				}
			}
			entries = append(entries, entry)
		}
	}
	sortLokiEntries(entries, backward)
	return entries, nil
}

// LogQLFilter selects one app's log lines. Empty fields are not filtered.
type LogQLFilter struct {
	App, TaskGroup, Task, Stream, Alloc, Contains string
}

// BuildLogQL builds the only LogQL Norn sends: a selector over Norn's own
// labels plus an optional line filter and structured-metadata matcher.
// Every value is emitted as a quoted literal (strconv.Quote escapes quotes,
// backslashes and control bytes), so no caller text becomes LogQL syntax.
func BuildLogQL(f LogQLFilter) string {
	var b strings.Builder
	b.WriteString(`{source="norn",app=` + strconv.Quote(f.App))
	for _, label := range []struct{ name, value string }{{"task_group", f.TaskGroup}, {"task", f.Task}, {"stream", f.Stream}} {
		if label.value != "" {
			b.WriteString("," + label.name + "=" + strconv.Quote(label.value))
		}
	}
	b.WriteString("}")
	if f.Contains != "" {
		b.WriteString(" |= " + strconv.Quote(f.Contains))
	}
	if f.Alloc != "" {
		b.WriteString(" | alloc_id=" + strconv.Quote(f.Alloc))
	}
	return b.String()
}

func sortLokiEntries(entries []LokiEntry, backward bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		if backward {
			return entries[i].Time.After(entries[j].Time)
		}
		return entries[i].Time.Before(entries[j].Time)
	})
}
