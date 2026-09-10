package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

const historyRetention = 30 * 24 * time.Hour
const historyMaxPoints = 720

type hostHistoryPoint struct {
	ObservedAt       time.Time `json:"observedAt"`
	CPUPercent       float64   `json:"cpuPercent"`
	MemoryUsedBytes  uint64    `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64    `json:"memoryTotalBytes"`
}
type serviceHistoryPoint struct {
	ObservedAt    time.Time `json:"observedAt"`
	App           string    `json:"app"`
	Process       string    `json:"process"`
	CPUPercent    float64   `json:"cpuPercent"`
	MemoryPercent float64   `json:"memoryPercent"`
}
type hostHistoryResponse struct {
	SchemaVersion    string                `json:"schemaVersion"`
	Source           string                `json:"source"`
	SourceSemantics  string                `json:"sourceSemantics"`
	Start            time.Time             `json:"start"`
	End              time.Time             `json:"end"`
	StepSeconds      int                   `json:"stepSeconds"`
	RetentionSeconds int                   `json:"retentionSeconds"`
	Host             []hostHistoryPoint    `json:"host"`
	Services         []serviceHistoryPoint `json:"services"`
}
type historyRange struct {
	start, end time.Time
	step       int
	services   bool
}

func parseHistoryRange(q url.Values, now time.Time) (historyRange, error) {
	var v historyRange
	var err error
	v.start, err = time.Parse(time.RFC3339, q.Get("start"))
	if err != nil {
		return v, errors.New("start must be RFC3339")
	}
	v.end, err = time.Parse(time.RFC3339, q.Get("end"))
	if err != nil {
		return v, errors.New("end must be RFC3339")
	}
	if !v.start.Before(now) || !v.end.After(v.start) || v.end.After(now.Add(time.Minute)) || v.start.Before(now.Add(-historyRetention-time.Minute)) {
		return v, errors.New("range must be within the last 30 days")
	}
	if v.start.Before(now.Add(-historyRetention)) {
		v.start = now.Add(-historyRetention)
	}
	if v.end.After(now) {
		v.end = now
	}
	v.step = 15
	if raw := q.Get("step"); raw != "" {
		v.step, err = strconv.Atoi(raw)
		if err != nil || v.step < 1 || v.step > int(historyRetention.Seconds()) {
			return v, errors.New("invalid step")
		}
	}
	floor := 15
	if now.Sub(v.start) > time.Hour+time.Minute {
		floor = 60
	}
	if now.Sub(v.start) > 24*time.Hour+time.Minute {
		floor = 300
	}
	v.step = max(v.step, floor, int(math.Ceil(v.end.Sub(v.start).Seconds()/float64(historyMaxPoints-1))))
	if raw := q.Get("includeServices"); raw != "" {
		v.services, err = strconv.ParseBool(raw)
		if err != nil {
			return v, errors.New("invalid includeServices")
		}
	}
	return v, nil
}

// HostMetricsHistory runs only fixed, host-scoped queries against a trusted
// monitoring origin. No request headers or credentials are forwarded.
func (h *Handler) HostMetricsHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	preventSensitiveResponseCaching(w)
	v, err := parseHistoryRange(r.URL.Query(), time.Now().UTC())
	if err != nil {
		WriteControlProblem(w, r, 400, "invalid_history_range", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	origin, host, exporter, err := h.historyOrigin(ctx, v.services)
	if err != nil {
		WriteControlProblem(w, r, 503, "host_history_unavailable", "historical monitoring is unavailable")
		return
	}
	out, err := fetchHostHistory(ctx, origin, host, exporter, v)
	if err != nil {
		WriteControlProblem(w, r, 503, "host_history_unavailable", "historical monitoring query failed")
		return
	}
	writeJSON(w, out)
}
func validHistoryOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid monitoring origin")
	}
	return u, nil
}
func (h *Handler) historyOrigin(ctx context.Context, services bool) (*url.URL, string, string, error) {
	host, _ := os.Hostname()
	raw := ""
	if h.cfg != nil {
		raw = h.cfg.HostMetricsPrometheusURL
		if h.cfg.HostMetricsHostname != "" {
			host = h.cfg.HostMetricsHostname
		}
	}
	// macOS commonly reports a .local suffix while Nomad uses the short name.
	host = strings.TrimSuffix(host, ".local")
	if host == "" {
		return nil, "", "", errors.New("host unavailable")
	}
	discover := func(name string, local bool) (string, error) {
		if h.consul == nil {
			return "", errors.New("discovery unavailable")
		}
		entries, _, err := h.consul.API().Health().Service(name, "", true, (&consulapi.QueryOptions{}).WithContext(ctx))
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if local && strings.TrimSuffix(e.Node.Node, ".local") != host {
				continue
			}
			address := e.Service.Address
			if address == "" {
				address = e.Node.Address
			}
			if address != "" && e.Service.Port > 0 {
				return net.JoinHostPort(address, strconv.Itoa(e.Service.Port)), nil
			}
		}
		return "", errors.New("service unavailable")
	}
	if raw == "" {
		address, err := discover("norn-prometheus-web", false)
		if err != nil {
			return nil, "", "", err
		}
		raw = "http://" + address
	}
	origin, err := validHistoryOrigin(raw)
	if err != nil {
		return nil, "", "", err
	}
	exporter := ""
	if services {
		exporter, err = discover("norn-cadvisor-docker-stats-metrics", true)
		if err != nil {
			return nil, "", "", err
		}
	}
	return origin, host, exporter, nil
}

type historyMatrix struct {
	Metric map[string]string   `json:"metric"`
	Values [][]json.RawMessage `json:"values"`
}

func historyQuery(ctx context.Context, origin *url.URL, query string, v historyRange) ([]historyMatrix, error) {
	u := *origin
	u.Path = "/api/v1/query_range"
	u.RawQuery = url.Values{"query": {query}, "start": {strconv.FormatInt(v.start.Unix(), 10)}, "end": {strconv.FormatInt(v.end.Unix(), 10)}, "step": {strconv.Itoa(v.step)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("monitoring response failed")
	}
	const limit = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return nil, errors.New("monitoring response too large")
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     []historyMatrix `json:"result"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.Status != "success" || out.Data.ResultType != "matrix" || len(out.Data.Result) > 128 {
		return nil, errors.New("invalid monitoring result")
	}
	for _, s := range out.Data.Result {
		if len(s.Values) > historyMaxPoints {
			return nil, errors.New("too many monitoring points")
		}
	}
	return out.Data.Result, nil
}
func historyValues(s historyMatrix) map[int64]float64 {
	out := map[int64]float64{}
	for _, pair := range s.Values {
		if len(pair) != 2 {
			continue
		}
		var ts float64
		var raw string
		if json.Unmarshal(pair[0], &ts) != nil || json.Unmarshal(pair[1], &raw) != nil {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}
		out[int64(ts)] = value
	}
	return out
}
func fetchHostHistory(ctx context.Context, origin *url.URL, host, exporter string, v historyRange) (hostHistoryResponse, error) {
	out := hostHistoryResponse{SchemaVersion: "norn.host-metrics-history/v1", Source: "nomad-prometheus", SourceSemantics: "Native Nomad host CPU averaged across cores; Nomad used memory. Coarse samples preserve bucket maxima. Service CPU and working-set memory are percentages of host capacity, scoped to the local exporter. Retention is an upper bound; monitoring storage limits may shorten it.", Start: v.start.UTC(), End: v.end.UTC(), StepSeconds: v.step, RetentionSeconds: int(historyRetention.Seconds()), Host: []hostHistoryPoint{}, Services: []serviceHistoryPoint{}}
	selector := fmt.Sprintf("{host=%q}", host)
	peak := func(q string) string {
		if v.step <= 15 {
			return q
		}
		return fmt.Sprintf("max_over_time((%s)[%ds:15s])", q, v.step)
	}
	queries := []string{peak("avg(nomad_client_host_cpu_total_percent" + selector + ")"), peak("max(nomad_client_host_memory_used" + selector + ")"), "max(nomad_client_host_memory_total" + selector + ")"}
	if v.services {
		address, _, err := net.SplitHostPort(exporter)
		if err != nil {
			return out, err
		}
		// Exporter ports change on deployment; preserve older samples on this host.
		sel := fmt.Sprintf("{instance=~%q,service=\"norn-cadvisor-docker-stats-metrics\",norn_app!=\"\",norn_process!=\"\"}", regexp.QuoteMeta(address)+":[0-9]+")
		queries = append(queries, peak("100 * sum by (norn_app,norn_process) (rate(norn_container_cpu_usage_seconds_total"+sel+"[2m])) / scalar(count(nomad_client_host_cpu_total_percent"+selector+"))"), peak("100 * sum by (norn_app,norn_process) (norn_container_memory_working_set_bytes"+sel+") / scalar(max(nomad_client_host_memory_total"+selector+"))"))
	}
	type result struct {
		index  int
		matrix []historyMatrix
		err    error
	}
	ch := make(chan result, len(queries))
	for i, q := range queries {
		go func(i int, q string) { m, e := historyQuery(ctx, origin, q, v); ch <- result{i, m, e} }(i, q)
	}
	results := make([][]historyMatrix, len(queries))
	for range queries {
		r := <-ch
		if r.err != nil {
			return out, r.err
		}
		results[r.index] = r.matrix
	}
	hostMaps := make([]map[int64]float64, 3)
	for i := 0; i < 3; i++ {
		if len(results[i]) == 0 {
			return out, nil
		}
		if len(results[i]) != 1 {
			return out, errors.New("ambiguous host metrics")
		}
		hostMaps[i] = historyValues(results[i][0])
	}
	for ts, cpu := range hostMaps[0] {
		used, uok := hostMaps[1][ts]
		total, tok := hostMaps[2][ts]
		if !uok || !tok || total <= 0 || total >= math.MaxUint64 || ts < v.start.Unix() || ts > v.end.Unix() {
			continue
		}
		out.Host = append(out.Host, hostHistoryPoint{time.Unix(ts, 0).UTC(), math.Min(cpu, 100), uint64(math.Min(used, total)), uint64(total)})
	}
	sort.Slice(out.Host, func(i, j int) bool { return out.Host[i].ObservedAt.Before(out.Host[j].ObservedAt) })
	if v.services {
		cpuMaps := map[string]map[int64]float64{}
		memMaps := map[string]map[int64]float64{}
		labels := map[string][2]string{}
		for index, matrices := range results[3:] {
			for _, series := range matrices {
				app, process := series.Metric["norn_app"], series.Metric["norn_process"]
				if app == "" || process == "" {
					continue
				}
				key := app + "\x00" + process
				labels[key] = [2]string{app, process}
				if index == 0 {
					cpuMaps[key] = historyValues(series)
				} else {
					memMaps[key] = historyValues(series)
				}
			}
		}
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		scores := make(map[string]float64, len(keys))
		for _, key := range keys {
			for _, value := range cpuMaps[key] {
				scores[key] = math.Max(scores[key], value)
			}
			for _, value := range memMaps[key] {
				scores[key] = math.Max(scores[key], value)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := scores[keys[i]], scores[keys[j]]
			if a == b {
				return keys[i] < keys[j]
			}
			return a > b
		})
		if len(keys) > 12 {
			keys = keys[:12]
		}
		for _, key := range keys {
			for ts, cpu := range cpuMaps[key] {
				mem, ok := memMaps[key][ts]
				if !ok || ts < v.start.Unix() || ts > v.end.Unix() {
					continue
				}
				l := labels[key]
				out.Services = append(out.Services, serviceHistoryPoint{time.Unix(ts, 0).UTC(), l[0], l[1], math.Min(cpu, 100), math.Min(mem, 100)})
			}
		}
		sort.Slice(out.Services, func(i, j int) bool {
			a, b := out.Services[i], out.Services[j]
			if a.ObservedAt.Equal(b.ObservedAt) {
				return a.App+a.Process < b.App+b.Process
			}
			return a.ObservedAt.Before(b.ObservedAt)
		})
	}
	return out, nil
}
