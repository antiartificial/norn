package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

const (
	defaultAccessPatternWindow = 14 * 24 * time.Hour
	defaultIdleCandidateAfter  = 7 * 24 * time.Hour
)

type accessObservationRequest struct {
	Observations []store.AccessObservation `json:"observations"`
}

type accessObservationReceipt struct {
	Recorded int `json:"recorded"`
}

type accessPatternSummary struct {
	App               string            `json:"app"`
	Process           string            `json:"process"`
	Type              string            `json:"type"`
	Status            string            `json:"status"`
	Endpoints         []string          `json:"endpoints,omitempty"`
	Sources           []string          `json:"sources,omitempty"`
	WindowHours       int               `json:"windowHours"`
	TotalRequests     int64             `json:"totalRequests"`
	Successes         int64             `json:"successes"`
	ClientErrors      int64             `json:"clientErrors"`
	ServerErrors      int64             `json:"serverErrors"`
	FirstSeen         *time.Time        `json:"firstSeen,omitempty"`
	LastSeen          *time.Time        `json:"lastSeen,omitempty"`
	QuietForHours     *float64          `json:"quietForHours,omitempty"`
	ActiveHours       int               `json:"activeHours"`
	ActiveWeekdays    []int             `json:"activeWeekdays,omitempty"`
	PeakHourUTC       *int              `json:"peakHourUtc,omitempty"`
	HourlyUTC         map[string]int64  `json:"hourlyUtc"`
	WeekdayUTC        map[string]int64  `json:"weekdayUtc"`
	IdleCandidate     bool              `json:"idleCandidate"`
	IdleReason        string            `json:"idleReason,omitempty"`
	RecommendedAction string            `json:"recommendedAction"`
	Confidence        string            `json:"confidence"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

func (h *Handler) RecordAccessObservations(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not connected")
		return
	}
	var req accessObservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid observation payload")
		return
	}
	if len(req.Observations) == 0 {
		writeError(w, http.StatusBadRequest, "at least one observation is required")
		return
	}
	if len(req.Observations) > 500 {
		writeError(w, http.StatusBadRequest, "observation batch is limited to 500")
		return
	}

	recorded := 0
	for _, obs := range req.Observations {
		if strings.TrimSpace(obs.App) == "" {
			writeError(w, http.StatusBadRequest, "observation app is required")
			return
		}
		if err := h.db.RecordAccessObservation(r.Context(), obs); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		recorded++
	}
	writeJSON(w, accessObservationReceipt{Recorded: recorded})
}

func (h *Handler) AccessPatterns(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not connected")
		return
	}
	window := durationQuery(r, "window", defaultAccessPatternWindow)
	idleAfter := durationQuery(r, "idleAfter", defaultIdleCandidateAfter)
	summaries, err := h.buildAccessPatternSummaries(r.Context(), window, idleAfter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{
		"windowHours":    int(window.Hours()),
		"idleAfterHours": int(idleAfter.Hours()),
		"patterns":       summaries,
	})
}

func (h *Handler) buildAccessPatternSummaries(ctx context.Context, window, idleAfter time.Duration) ([]accessPatternSummary, error) {
	manifest, err := h.buildServiceManifest()
	if err != nil {
		return nil, err
	}
	rows, err := h.db.ListAccessPatternRows(ctx, time.Now().UTC().Add(-window))
	if err != nil {
		return nil, err
	}
	return summarizeAccessPatterns(manifest.Services, rows, window, idleAfter, time.Now().UTC()), nil
}

func summarizeAccessPatterns(services []model.ServiceManifestEntry, rows []store.AccessPatternRow, window, idleAfter time.Duration, now time.Time) []accessPatternSummary {
	byKey := map[string]*accessPatternSummary{}
	for _, service := range services {
		if service.Type != "service" {
			continue
		}
		key := accessPatternKey(service.App, service.Process)
		endpoints := make([]string, 0, len(service.Endpoints))
		for _, endpoint := range service.Endpoints {
			endpoints = append(endpoints, endpoint.URL)
		}
		byKey[key] = &accessPatternSummary{
			App:               service.App,
			Process:           service.Process,
			Type:              service.Type,
			Status:            service.Status,
			Endpoints:         endpoints,
			WindowHours:       int(window.Hours()),
			HourlyUTC:         emptyBucketMap(24),
			WeekdayUTC:        emptyBucketMap(7),
			RecommendedAction: "observe",
			Confidence:        "none",
			Metadata:          service.Metadata,
		}
	}

	sourceSets := map[string]map[string]bool{}
	weekdaySets := map[string]map[int]bool{}
	activeHourSets := map[string]map[string]bool{}
	endpointSets := map[string]map[string]bool{}
	for _, row := range rows {
		key := accessPatternKey(row.App, row.Process)
		summary, ok := byKey[key]
		if !ok {
			summary = &accessPatternSummary{
				App:               row.App,
				Process:           row.Process,
				Type:              "unknown",
				Status:            "unknown",
				WindowHours:       int(window.Hours()),
				HourlyUTC:         emptyBucketMap(24),
				WeekdayUTC:        emptyBucketMap(7),
				RecommendedAction: "observe",
				Confidence:        "low",
			}
			byKey[key] = summary
		}
		summary.TotalRequests += row.Requests
		summary.Successes += row.Successes
		summary.ClientErrors += row.ClientErrors
		summary.ServerErrors += row.ServerErrors
		if summary.FirstSeen == nil || row.FirstSeen.Before(*summary.FirstSeen) {
			t := row.FirstSeen
			summary.FirstSeen = &t
		}
		if summary.LastSeen == nil || row.LastSeen.After(*summary.LastSeen) {
			t := row.LastSeen
			summary.LastSeen = &t
		}
		hourKey := strconv.Itoa(row.Hour)
		weekdayKey := strconv.Itoa(row.Weekday)
		summary.HourlyUTC[hourKey] += row.Requests
		summary.WeekdayUTC[weekdayKey] += row.Requests
		setString(sourceSets, key, row.Source)
		setString(endpointSets, key, row.Endpoint)
		setInt(weekdaySets, key, row.Weekday)
		setString(activeHourSets, key, strconv.Itoa(row.Weekday)+"-"+strconv.Itoa(row.Hour))
	}

	summaries := make([]accessPatternSummary, 0, len(byKey))
	for key, summary := range byKey {
		summary.Sources = sortedStrings(sourceSets[key])
		if len(summary.Endpoints) == 0 {
			summary.Endpoints = sortedStrings(endpointSets[key])
		}
		summary.ActiveWeekdays = sortedInts(weekdaySets[key])
		summary.ActiveHours = len(activeHourSets[key])
		summary.PeakHourUTC = peakHour(summary.HourlyUTC)
		classifyIdleCandidate(summary, idleAfter, now)
		summaries = append(summaries, *summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].IdleCandidate != summaries[j].IdleCandidate {
			return summaries[i].IdleCandidate
		}
		if summaries[i].TotalRequests != summaries[j].TotalRequests {
			return summaries[i].TotalRequests < summaries[j].TotalRequests
		}
		if summaries[i].App != summaries[j].App {
			return summaries[i].App < summaries[j].App
		}
		return summaries[i].Process < summaries[j].Process
	})
	return summaries
}

func classifyIdleCandidate(summary *accessPatternSummary, idleAfter time.Duration, now time.Time) {
	if summary.TotalRequests == 0 {
		summary.IdleCandidate = true
		summary.IdleReason = "no access observations in window"
		summary.RecommendedAction = "observe_before_idle"
		summary.Confidence = "low"
		return
	}
	if summary.LastSeen != nil {
		quietFor := now.Sub(*summary.LastSeen).Hours()
		summary.QuietForHours = &quietFor
		if now.Sub(*summary.LastSeen) >= idleAfter {
			summary.IdleCandidate = true
			summary.IdleReason = "no observed access since idle threshold"
			summary.RecommendedAction = "consider_idle"
			summary.Confidence = "medium"
			return
		}
	}
	summary.RecommendedAction = "keep_warm"
	summary.Confidence = "medium"
}

func durationQuery(r *http.Request, key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
		return parsed
	}
	if strings.HasSuffix(raw, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(raw, "d")); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	if days, err := strconv.Atoi(raw); err == nil && days > 0 {
		return time.Duration(days) * 24 * time.Hour
	}
	return fallback
}

func accessPatternKey(app, process string) string {
	return strings.TrimSpace(app) + "\x00" + strings.TrimSpace(process)
}

func emptyBucketMap(size int) map[string]int64 {
	out := make(map[string]int64, size)
	for i := 0; i < size; i++ {
		out[strconv.Itoa(i)] = 0
	}
	return out
}

func setString(sets map[string]map[string]bool, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if sets[key] == nil {
		sets[key] = map[string]bool{}
	}
	sets[key][value] = true
}

func setInt(sets map[string]map[int]bool, key string, value int) {
	if sets[key] == nil {
		sets[key] = map[int]bool{}
	}
	sets[key][value] = true
}

func sortedStrings(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedInts(values map[int]bool) []int {
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func peakHour(hourly map[string]int64) *int {
	var peak *int
	var peakValue int64
	for raw, value := range hourly {
		hour, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		if peak == nil || value > peakValue {
			h := hour
			peak = &h
			peakValue = value
		}
	}
	if peakValue == 0 {
		return nil
	}
	return peak
}
