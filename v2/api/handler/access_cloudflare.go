package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

const (
	cloudflareMaxGraphQLWindow   = 24 * time.Hour
	cloudflareMaxGraphQLLookback = 7 * 24 * time.Hour
	cloudflareGraphQLQuery       = `
query NornRequestsByHostname($zoneTag: string, $filter: filter) {
  viewer {
    zones(filter: {zoneTag: $zoneTag}) {
      httpRequestsAdaptiveGroups(limit: 10000, filter: $filter, orderBy: [datetimeHour_ASC]) {
        count
        dimensions {
          datetimeHour
        }
      }
    }
  }
}`
)

type cloudflareStatusResponse struct {
	Configured         bool     `json:"configured"`
	APITokenConfigured bool     `json:"apiTokenConfigured"`
	ZoneIDConfigured   bool     `json:"zoneIdConfigured"`
	LogpushConfigured  bool     `json:"logpushConfigured"`
	HostnameCount      int      `json:"hostnameCount"`
	Hostnames          []string `json:"hostnames,omitempty"`
}

type cloudflareSyncReceipt struct {
	WindowHours int                      `json:"windowHours"`
	Since       time.Time                `json:"since"`
	Until       time.Time                `json:"until"`
	Hosts       []cloudflareHostSyncInfo `json:"hosts"`
	Recorded    int                      `json:"recorded"`
	Skipped     int                      `json:"skipped"`
	Errors      []string                 `json:"errors,omitempty"`
}

type cloudflareHostSyncInfo struct {
	Hostname string `json:"hostname"`
	App      string `json:"app"`
	Process  string `json:"process"`
	Recorded int    `json:"recorded"`
	Error    string `json:"error,omitempty"`
}

type cloudflareLogpushReceipt struct {
	Received      int      `json:"received"`
	Recorded      int      `json:"recorded"`
	Skipped       int      `json:"skipped"`
	UnknownHosts  []string `json:"unknownHosts,omitempty"`
	InvalidEvents int      `json:"invalidEvents,omitempty"`
}

type accessHostTarget struct {
	App      string
	Process  string
	Endpoint string
}

type cloudflareGraphQLResponse struct {
	Data struct {
		Viewer struct {
			Zones []struct {
				HTTPRequestsAdaptiveGroups []struct {
					Count      int64 `json:"count"`
					Dimensions struct {
						DatetimeHour time.Time `json:"datetimeHour"`
					} `json:"dimensions"`
				} `json:"httpRequestsAdaptiveGroups"`
			} `json:"zones"`
		} `json:"viewer"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

func (h *Handler) CloudflareAccessStatus(w http.ResponseWriter, r *http.Request) {
	hostMap, err := h.accessHostnameMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hostnames := sortedHostnames(hostMap)
	resp := cloudflareStatusResponse{
		Configured:         h.cfg.CloudflareAPIToken != "" && h.cfg.CloudflareZoneID != "",
		APITokenConfigured: h.cfg.CloudflareAPIToken != "",
		ZoneIDConfigured:   h.cfg.CloudflareZoneID != "",
		LogpushConfigured:  h.cfg.CloudflareLogpushToken != "",
		HostnameCount:      len(hostnames),
		Hostnames:          hostnames,
	}
	writeJSON(w, resp)
}

func (h *Handler) CloudflareAccessSync(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not connected")
		return
	}
	if h.cfg.CloudflareAPIToken == "" || h.cfg.CloudflareZoneID == "" {
		writeError(w, http.StatusBadRequest, "cloudflare api token and zone id are not configured")
		return
	}
	hostMap, err := h.accessHostnameMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(hostMap) == 0 {
		writeError(w, http.StatusBadRequest, "no public hostnames found in service manifest")
		return
	}
	window := durationQuery(r, "window", defaultAccessPatternWindow)
	until := time.Now().UTC().Truncate(time.Hour)
	since := cloudflareEffectiveSince(until.Add(-window), until, cloudflareMaxGraphQLLookback)
	receipt := cloudflareSyncReceipt{
		WindowHours: int(until.Sub(since).Hours()),
		Since:       since,
		Until:       until,
	}
	for _, hostname := range sortedHostnames(hostMap) {
		target := hostMap[hostname]
		recorded, err := h.syncCloudflareHostWindow(r.Context(), hostname, target, since, until)
		info := cloudflareHostSyncInfo{
			Hostname: hostname,
			App:      target.App,
			Process:  target.Process,
			Recorded: recorded,
		}
		if err != nil {
			info.Error = err.Error()
			receipt.Errors = append(receipt.Errors, fmt.Sprintf("%s: %v", hostname, err))
		}
		receipt.Recorded += recorded
		receipt.Hosts = append(receipt.Hosts, info)
	}
	writeJSON(w, receipt)
}

func (h *Handler) syncCloudflareHostWindow(ctx context.Context, hostname string, target accessHostTarget, since, until time.Time) (int, error) {
	recorded := 0
	for _, chunk := range cloudflareSyncChunks(since, until, cloudflareMaxGraphQLWindow) {
		chunkRecorded, err := h.syncCloudflareHost(ctx, hostname, target, chunk.Since, chunk.Until)
		recorded += chunkRecorded
		if err != nil {
			return recorded, err
		}
	}
	return recorded, nil
}

func (h *Handler) CloudflareLogpush(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not connected")
		return
	}
	if h.cfg.CloudflareLogpushToken == "" {
		writeError(w, http.StatusServiceUnavailable, "cloudflare logpush token is not configured")
		return
	}
	if !validLogpushToken(r, h.cfg.CloudflareLogpushToken) {
		writeError(w, http.StatusUnauthorized, "invalid cloudflare logpush token")
		return
	}
	hostMap, err := h.accessHostnameMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	body, err := readPossiblyGzippedBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	events, invalid, err := parseCloudflareLogpushEvents(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	receipt := cloudflareLogpushReceipt{Received: len(events), InvalidEvents: invalid}
	unknown := map[string]bool{}
	for _, event := range events {
		target, ok := hostMap[event.Host]
		if !ok {
			receipt.Skipped++
			unknown[event.Host] = true
			continue
		}
		obs := store.AccessObservation{
			App:        target.App,
			Process:    target.Process,
			Endpoint:   target.Endpoint,
			Source:     "cloudflare-logpush",
			ObservedAt: event.ObservedAt,
			Count:      1,
			Status:     event.Status,
		}
		if err := h.db.RecordAccessObservation(r.Context(), obs); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		receipt.Recorded++
	}
	receipt.UnknownHosts = sortedBoolKeys(unknown)
	writeJSON(w, receipt)
}

type cloudflareSyncChunk struct {
	Since time.Time
	Until time.Time
}

func cloudflareSyncChunks(since, until time.Time, maxWindow time.Duration) []cloudflareSyncChunk {
	since = since.UTC()
	until = until.UTC()
	if !since.Before(until) {
		return nil
	}
	if maxWindow <= 0 {
		maxWindow = cloudflareMaxGraphQLWindow
	}
	var chunks []cloudflareSyncChunk
	for start := since; start.Before(until); {
		end := start.Add(maxWindow)
		if end.After(until) {
			end = until
		}
		chunks = append(chunks, cloudflareSyncChunk{Since: start, Until: end})
		start = end
	}
	return chunks
}

func cloudflareEffectiveSince(requestedSince, until time.Time, maxLookback time.Duration) time.Time {
	requestedSince = requestedSince.UTC()
	until = until.UTC()
	if maxLookback <= 0 {
		return requestedSince
	}
	earliest := until.Add(-maxLookback)
	if requestedSince.Before(earliest) {
		return earliest
	}
	return requestedSince
}

func (h *Handler) syncCloudflareHost(ctx context.Context, hostname string, target accessHostTarget, since, until time.Time) (int, error) {
	payload := map[string]interface{}{
		"query": cloudflareGraphQLQuery,
		"variables": map[string]interface{}{
			"zoneTag": h.cfg.CloudflareZoneID,
			"filter": map[string]interface{}{
				"datetime_geq":          since.Format(time.RFC3339),
				"datetime_lt":           until.Format(time.RFC3339),
				"clientRequestHTTPHost": hostname,
				"requestSource":         "eyeball",
			},
		},
	}
	body, _ := json.Marshal(payload)
	baseURL := strings.TrimRight(h.cfg.CloudflareAPIBaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.CloudflareAPIToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("cloudflare graphql returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var decoded cloudflareGraphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return 0, err
	}
	if len(decoded.Errors) > 0 {
		return 0, fmt.Errorf("cloudflare graphql error: %s", decoded.Errors[0].Message)
	}
	if len(decoded.Data.Viewer.Zones) == 0 {
		return 0, nil
	}
	recorded := 0
	for _, group := range decoded.Data.Viewer.Zones[0].HTTPRequestsAdaptiveGroups {
		if group.Count <= 0 || group.Dimensions.DatetimeHour.IsZero() {
			continue
		}
		obs := store.AccessObservation{
			App:        target.App,
			Process:    target.Process,
			Endpoint:   target.Endpoint,
			Source:     "cloudflare-graphql",
			ObservedAt: group.Dimensions.DatetimeHour,
			Count:      group.Count,
			Status:     200,
		}
		if err := h.db.ReplaceAccessObservation(ctx, obs); err != nil {
			return recorded, err
		}
		recorded++
	}
	return recorded, nil
}

func (h *Handler) accessHostnameMap() (map[string]accessHostTarget, error) {
	manifest, err := h.buildServiceManifest()
	if err != nil {
		return nil, err
	}
	return accessHostnameMap(manifest.Services), nil
}

func accessHostnameMap(services []model.ServiceManifestEntry) map[string]accessHostTarget {
	out := map[string]accessHostTarget{}
	for _, service := range services {
		if service.Type != "service" {
			continue
		}
		for _, endpoint := range service.Endpoints {
			hostname := endpointHostname(endpoint.URL)
			if hostname == "" {
				continue
			}
			out[hostname] = accessHostTarget{
				App:      service.App,
				Process:  service.Process,
				Endpoint: endpoint.URL,
			}
		}
	}
	return out
}

func endpointHostname(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".ts.net") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return ""
	}
	return host
}

func sortedHostnames(hostMap map[string]accessHostTarget) []string {
	out := make([]string, 0, len(hostMap))
	for hostname := range hostMap {
		out = append(out, hostname)
	}
	sort.Strings(out)
	return out
}

func sortedBoolKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

type cloudflareLogpushEvent struct {
	Host       string
	ObservedAt time.Time
	Status     int
}

func parseCloudflareLogpushEvents(body []byte) ([]cloudflareLogpushEvent, int, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, 0, fmt.Errorf("empty logpush payload")
	}
	var rawEvents []map[string]interface{}
	if body[0] == '[' {
		if err := json.Unmarshal(body, &rawEvents); err != nil {
			return nil, 0, fmt.Errorf("invalid logpush json array")
		}
	} else {
		lines := bytes.Split(body, []byte("\n"))
		for _, line := range lines {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var raw map[string]interface{}
			if err := json.Unmarshal(line, &raw); err != nil {
				return nil, 0, fmt.Errorf("invalid logpush ndjson")
			}
			rawEvents = append(rawEvents, raw)
		}
	}
	events := make([]cloudflareLogpushEvent, 0, len(rawEvents))
	invalid := 0
	for _, raw := range rawEvents {
		event, ok := parseCloudflareLogpushEvent(raw)
		if !ok {
			invalid++
			continue
		}
		events = append(events, event)
	}
	return events, invalid, nil
}

func parseCloudflareLogpushEvent(raw map[string]interface{}) (cloudflareLogpushEvent, bool) {
	host := strings.ToLower(strings.TrimSpace(firstStringField(raw, "ClientRequestHost", "clientRequestHost", "Host", "host")))
	if host == "" {
		return cloudflareLogpushEvent{}, false
	}
	status := firstIntField(raw, "EdgeResponseStatus", "edgeResponseStatus", "OriginResponseStatus", "originResponseStatus", "Status", "status")
	observedAt := firstTimeField(raw, "EdgeStartTimestamp", "edgeStartTimestamp", "Datetime", "datetime", "Timestamp", "timestamp")
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return cloudflareLogpushEvent{Host: host, ObservedAt: observedAt.UTC(), Status: status}, true
}

func firstStringField(raw map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if s, ok := value.(string); ok {
				return s
			}
		}
	}
	return ""
}

func firstIntField(raw map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			switch v := value.(type) {
			case float64:
				return int(v)
			case int:
				return v
			case string:
				var out int
				if _, err := fmt.Sscanf(v, "%d", &out); err == nil {
					return out
				}
			}
		}
	}
	return 200
}

func firstTimeField(raw map[string]interface{}, keys ...string) time.Time {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			switch v := value.(type) {
			case string:
				if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
					return parsed
				}
			case float64:
				return time.Unix(0, int64(v)).UTC()
			}
		}
	}
	return time.Time{}
}

func readPossiblyGzippedBody(r *http.Request) ([]byte, error) {
	var reader io.Reader = r.Body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("invalid gzip payload")
		}
		defer gz.Close()
		reader = gz
	}
	return io.ReadAll(io.LimitReader(reader, 20<<20))
}

func validLogpushToken(r *http.Request, expected string) bool {
	token := strings.TrimSpace(r.Header.Get("X-Norn-Logpush-Token"))
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Logpush-Secret"))
	}
	if token == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			token = strings.TrimSpace(auth[7:])
		}
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}
